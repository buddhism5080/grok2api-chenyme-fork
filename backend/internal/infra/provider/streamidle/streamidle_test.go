package streamidle

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/pkg/neterror"
)

type contextReader struct{ ctx context.Context }

func (r contextReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func (contextReader) Close() error { return nil }

func TestReadCloserCancelsBlockedReadWithSharedSentinel(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	wrapper := New(contextReader{ctx: ctx}, 0, 20*time.Millisecond, cancel)
	defer wrapper.Close()

	_, err := wrapper.Read(make([]byte, 1))
	if !errors.Is(err, neterror.ErrUpstreamStreamIdleTimeout) {
		t.Fatalf("Read() error = %v, want ErrUpstreamStreamIdleTimeout", err)
	}
	if cause := context.Cause(ctx); !errors.Is(cause, neterror.ErrUpstreamStreamIdleTimeout) {
		t.Fatalf("context cause = %v, want ErrUpstreamStreamIdleTimeout", cause)
	}
	if !wrapper.TimedOut() {
		t.Fatal("TimedOut() = false after idle deadline")
	}
}

func TestReadCloserResetsDeadlineOnProgress(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	wrapper := New(io.NopCloser(&pacedReader{chunks: 3, gap: 10 * time.Millisecond}), 0, 30*time.Millisecond, cancel)
	defer wrapper.Close()
	if _, err := io.Copy(io.Discard, wrapper); err != nil {
		t.Fatal(err)
	}
	if wrapper.TimedOut() || context.Cause(ctx) != nil {
		t.Fatalf("steady stream timed out: timedOut=%t cause=%v", wrapper.TimedOut(), context.Cause(ctx))
	}
}

func TestReadCloserReportsProgressBeforeIdleTimeout(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	reader, writer := io.Pipe()
	wrapper := New(reader, 0, 30*time.Millisecond, cancel)
	defer wrapper.Close()

	go func() {
		_, _ = writer.Write([]byte("{"))
		<-ctx.Done()
		_ = writer.CloseWithError(ctx.Err())
	}()

	buffer := make([]byte, 1)
	if n, err := wrapper.Read(buffer); n != 1 || err != nil {
		t.Fatalf("first read = (%d, %v), want one byte", n, err)
	}
	if _, err := wrapper.Read(buffer); !errors.Is(err, neterror.ErrUpstreamStreamIdleTimeout) || !neterror.IdleTimeoutObservedData(err) {
		t.Fatalf("idle error = %#v, observed=%t", err, neterror.IdleTimeoutObservedData(err))
	}
}

func TestReadCloserFirstCharTimeoutBeforeAnyData(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	wrapper := New(contextReader{ctx: ctx}, 20*time.Millisecond, time.Hour, cancel)
	defer wrapper.Close()

	_, err := wrapper.Read(make([]byte, 1))
	if !errors.Is(err, neterror.ErrUpstreamFirstCharTimeout) {
		t.Fatalf("Read() error = %v, want ErrUpstreamFirstCharTimeout", err)
	}
	if cause := context.Cause(ctx); !errors.Is(cause, neterror.ErrUpstreamFirstCharTimeout) {
		t.Fatalf("context cause = %v, want ErrUpstreamFirstCharTimeout", cause)
	}
	if !wrapper.TimedOut() {
		t.Fatal("TimedOut() = false after first-char deadline")
	}
}

func TestReadCloserIdleTimeoutAfterFirstByte(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	// One byte immediately, then block on ctx until idle timer cancels.
	body := &firstThenBlock{ctx: ctx, data: []byte("x")}
	wrapper := New(body, time.Hour, 25*time.Millisecond, cancel)
	defer wrapper.Close()

	n, err := wrapper.Read(make([]byte, 1))
	if n != 1 || err != nil {
		t.Fatalf("first Read() = %d, %v", n, err)
	}
	_, err = wrapper.Read(make([]byte, 1))
	if !errors.Is(err, neterror.ErrUpstreamStreamIdleTimeout) {
		t.Fatalf("second Read() error = %v, want ErrUpstreamStreamIdleTimeout", err)
	}
	if cause := context.Cause(ctx); !errors.Is(cause, neterror.ErrUpstreamStreamIdleTimeout) {
		t.Fatalf("context cause = %v, want ErrUpstreamStreamIdleTimeout", cause)
	}
}

type firstThenBlock struct {
	ctx  context.Context
	data []byte
	done bool
}

func (r *firstThenBlock) Read(buffer []byte) (int, error) {
	if !r.done && len(r.data) > 0 {
		r.done = true
		return copy(buffer, r.data), nil
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func (firstThenBlock) Close() error { return nil }

type pacedReader struct {
	chunks int
	gap    time.Duration
}

func (r *pacedReader) Read(buffer []byte) (int, error) {
	if r.chunks == 0 {
		return 0, io.EOF
	}
	time.Sleep(r.gap)
	r.chunks--
	buffer[0] = 'x'
	return 1, nil
}
