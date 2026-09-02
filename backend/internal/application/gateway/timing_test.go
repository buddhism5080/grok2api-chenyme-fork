package gateway

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/pkg/neterror"
)

func TestGenerationTimingLogsOnlyPhaseMetadata(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	timing := newGenerationTiming("public-model", accountdomain.ProviderBuild)
	timing.markSelection(10 * time.Millisecond)
	timing.markCredential(20 * time.Millisecond)
	timing.markUpstream(30 * time.Millisecond)
	timing.markUpstream(40 * time.Millisecond)
	body := &firstByteReadCloser{ReadCloser: io.NopCloser(strings.NewReader("ok")), mark: timing.markFirstBody}
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	timing.finish(logger, "success")
	logged := output.String()
	for _, expected := range []string{"generation_timing", "route=public-model", "provider=grok_build", "selection_wait_ms=10", "credential_wait_ms=20", "upstream_wait_ms=70", "attempts=2", "retries=1"} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("log missing %q: %s", expected, logged)
		}
	}
}

func TestFirstTokenTimerMarksOnce(t *testing.T) {
	timer := newFirstTokenTimer(time.Now().Add(-25 * time.Millisecond))
	if timer.milliseconds() != nil {
		t.Fatal("unmarked timer returned a value")
	}
	timer.mark()
	first := timer.milliseconds()
	if first == nil || *first < 20 {
		t.Fatalf("first token milliseconds = %v", first)
	}
	time.Sleep(time.Millisecond)
	timer.mark()
	second := timer.milliseconds()
	if second == nil || *second != *first {
		t.Fatalf("timer changed after second mark: first=%v second=%v", first, second)
	}
}

func TestFirstTokenDeadlineTimesOutWithoutMark(t *testing.T) {
	stall := newStallReadCloser()
	body, _ := armFirstTokenDeadline(stall, 30*time.Millisecond)
	defer body.Close()
	started := time.Now()
	_, err := body.Read(make([]byte, 8))
	if !errors.Is(err, neterror.ErrUpstreamFirstCharTimeout) {
		t.Fatalf("Read() error = %v, want ErrUpstreamFirstCharTimeout", err)
	}
	if elapsed := time.Since(started); elapsed < 25*time.Millisecond || elapsed > time.Second {
		t.Fatalf("timeout elapsed = %s", elapsed)
	}
}

func TestFirstTokenDeadlineCancelledByMark(t *testing.T) {
	body, note := armFirstTokenDeadline(io.NopCloser(strings.NewReader("hello")), 200*time.Millisecond)
	note()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("data = %q", data)
	}
	time.Sleep(250 * time.Millisecond)
	if body.timedOut.Load() {
		t.Fatal("deadline still fired after first-token mark")
	}
}

type stallReadCloser struct {
	closed chan struct{}
}

func newStallReadCloser() *stallReadCloser {
	return &stallReadCloser{closed: make(chan struct{})}
}

func (s *stallReadCloser) Read([]byte) (int, error) {
	<-s.closed
	return 0, io.EOF
}

func (s *stallReadCloser) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

// delayedReadCloser sleeps delay before yielding payload. Close unblocks the
// wait so a first-char timeout on this attempt cannot leak into the next one.
type delayedReadCloser struct {
	delay   time.Duration
	payload string
	once    sync.Once
	reader  *strings.Reader
	closed  atomic.Bool
	stop    chan struct{}
}

func newDelayedReadCloser(delay time.Duration, payload string) *delayedReadCloser {
	return &delayedReadCloser{delay: delay, payload: payload, stop: make(chan struct{})}
}

func (d *delayedReadCloser) Read(buffer []byte) (int, error) {
	if d.closed.Load() {
		return 0, io.EOF
	}
	d.once.Do(func() {
		timer := time.NewTimer(d.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			d.reader = strings.NewReader(d.payload)
		case <-d.stop:
		}
	})
	if d.closed.Load() || d.reader == nil {
		return 0, io.EOF
	}
	return d.reader.Read(buffer)
}

func (d *delayedReadCloser) Close() error {
	if d.closed.CompareAndSwap(false, true) {
		close(d.stop)
	}
	return nil
}
