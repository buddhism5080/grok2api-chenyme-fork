package gateway

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/pkg/neterror"
)

func TestAwaitFirstContentTokenTimesOutWithoutDelta(t *testing.T) {
	// Pure silence until closed by the deadline.
	stall := newStallReadCloser()
	_, _, err := awaitFirstContentToken(stall, 40*time.Millisecond, firstTokenKindResponses)
	if !errors.Is(err, neterror.ErrUpstreamFirstCharTimeout) {
		t.Fatalf("err = %v, want first-char timeout", err)
	}
}

func TestAwaitFirstContentTokenIgnoresMetadataUntilDeadline(t *testing.T) {
	// Metadata alone must not clear the first-token deadline.
	meta := "data: {\"type\":\"response.created\"}\n\n"
	stall := newStallReadCloser()
	body := io.MultiReader(strings.NewReader(meta), stall)
	// MultiReader doesn't close stall; wrap.
	closer := &multiClose{Reader: body, closer: stall}
	_, _, err := awaitFirstContentToken(closer, 50*time.Millisecond, firstTokenKindResponses)
	if !errors.Is(err, neterror.ErrUpstreamFirstCharTimeout) {
		t.Fatalf("err = %v, want first-char timeout after metadata-only stream", err)
	}
}

type multiClose struct {
	io.Reader
	closer io.Closer
}

func (m *multiClose) Close() error {
	if m.closer == nil {
		return nil
	}
	return m.closer.Close()
}

func TestAwaitFirstContentTokenSeesReasoningDelta(t *testing.T) {
	payload := "" +
		"data: {\"type\":\"response.created\"}\n\n" +
		"data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"think\"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"
	prefix, rest, err := awaitFirstContentToken(io.NopCloser(strings.NewReader(payload)), time.Second, firstTokenKindResponses)
	if err != nil {
		t.Fatal(err)
	}
	defer rest.Close()
	if !strings.Contains(string(prefix), "reasoning_text.delta") {
		t.Fatalf("prefix missing reasoning delta: %q", prefix)
	}
	// Remaining body should still contain the later output delta.
	leftover, _ := io.ReadAll(rest)
	combined := string(prefix) + string(leftover)
	if !strings.Contains(combined, "output_text.delta") {
		t.Fatalf("combined stream missing output delta: %q", combined)
	}
}

func TestAwaitFirstContentTokenSeesChatReasoning(t *testing.T) {
	payload := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"r\"}}]}\n\n"
	prefix, rest, err := awaitFirstContentToken(io.NopCloser(strings.NewReader(payload)), time.Second, firstTokenKindChat)
	if err != nil {
		t.Fatal(err)
	}
	defer rest.Close()
	if len(prefix) == 0 {
		t.Fatal("empty prefix")
	}
}

func TestAwaitFirstContentTokenRetriesEmptyCapacity(t *testing.T) {
	_, rest, err := awaitFirstContentToken(io.NopCloser(strings.NewReader(immediateCapacitySSE)), time.Second, firstTokenKindResponses)
	if rest != nil {
		_ = rest.Close()
	}
	if !errors.Is(err, errUpstreamModelAtCapacity) {
		t.Fatalf("err = %v, want errUpstreamModelAtCapacity", err)
	}
}
