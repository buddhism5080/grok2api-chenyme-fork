package gateway

import (
	"errors"
	"io"
	"strings"
	"testing"
)

const emptyCapacitySSE = "" +
	": keepalive\n\n" +
	": keepalive\n\n" +
	"event: error\n" +
	`data: {"sequence_number":0,"type":"error","code":null,"message":"The model is currently at capacity due to high demand. Please try again in a few minutes, or use a higher service tier for priority processing: https://docs.x.ai/developers/advanced-api-usage/priority-processing","param":null}` +
	"\n\n"

const immediateCapacitySSE = "" +
	"event: error\n" +
	`data: {"sequence_number":0,"type":"error","code":null,"message":"The model is currently at capacity due to high demand. Please try again in a few minutes.","param":null}` +
	"\n\n"

const anthropicCapacitySSE = "" +
	"event: error\n" +
	`data: {"type":"error","error":{"message":"The model is currently at capacity due to high demand.","type":"api_error"}}` +
	"\n\n"

const tokenGenerationErrorSSE = "" +
	"event: error\n" +
	`data: {"sequence_number":71,"type":"error","code":null,"message":"Internal error during token generation","param":null}` +
	"\n\n"

const outputThenCapacitySSE = "" +
	`data: {"type":"response.output_text.delta","delta":"hello"}` + "\n\n" +
	"event: error\n" +
	`data: {"type":"error","message":"The model is currently at capacity due to high demand."}` +
	"\n\n"

func TestPeekEmptyCapacityStreamRetriesKeepaliveThenCapacity(t *testing.T) {
	prefix, rest, err := peekEmptyCapacityStream(io.NopCloser(strings.NewReader(emptyCapacitySSE)))
	if rest != nil {
		_ = rest.Close()
	}
	if !errors.Is(err, errUpstreamModelAtCapacity) {
		t.Fatalf("err = %v, want errUpstreamModelAtCapacity (prefix=%q)", err, prefix)
	}
}

func TestPeekEmptyCapacityStreamRetriesImmediateCapacity(t *testing.T) {
	_, rest, err := peekEmptyCapacityStream(io.NopCloser(strings.NewReader(immediateCapacitySSE)))
	if rest != nil {
		_ = rest.Close()
	}
	if !errors.Is(err, errUpstreamModelAtCapacity) {
		t.Fatalf("err = %v, want errUpstreamModelAtCapacity", err)
	}
}

func TestPeekEmptyCapacityStreamRetriesAnthropicEnvelope(t *testing.T) {
	_, rest, err := peekEmptyCapacityStream(io.NopCloser(strings.NewReader(anthropicCapacitySSE)))
	if rest != nil {
		_ = rest.Close()
	}
	if !errors.Is(err, errUpstreamModelAtCapacity) {
		t.Fatalf("err = %v, want errUpstreamModelAtCapacity", err)
	}
}

func TestPeekEmptyCapacityStreamDoesNotRetryOtherErrors(t *testing.T) {
	prefix, rest, err := peekEmptyCapacityStream(io.NopCloser(strings.NewReader(tokenGenerationErrorSSE)))
	if rest != nil {
		defer rest.Close()
	}
	if err != nil {
		t.Fatalf("other SSE errors must hand off, err = %v", err)
	}
	leftover, _ := io.ReadAll(rest)
	combined := string(prefix) + string(leftover)
	if !strings.Contains(combined, "Internal error during token generation") {
		t.Fatalf("handed-off stream missing original error: %q", combined)
	}
}

func TestPeekEmptyCapacityStreamDoesNotRetryAfterOutput(t *testing.T) {
	prefix, rest, err := peekEmptyCapacityStream(io.NopCloser(strings.NewReader(outputThenCapacitySSE)))
	if rest != nil {
		defer rest.Close()
	}
	if err != nil {
		t.Fatalf("content then capacity must hand off, err = %v", err)
	}
	leftover, _ := io.ReadAll(rest)
	combined := string(prefix) + string(leftover)
	if !strings.Contains(combined, `"delta":"hello"`) {
		t.Fatalf("handed-off stream missing output delta: %q", combined)
	}
}
