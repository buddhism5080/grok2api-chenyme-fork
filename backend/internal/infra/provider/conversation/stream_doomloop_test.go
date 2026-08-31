package conversation

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/pkg/neterror"
)

type blockingStreamSource struct {
	closed chan struct{}
}

func (s *blockingStreamSource) Read([]byte) (int, error) {
	<-s.closed
	return 0, io.EOF
}

func (s *blockingStreamSource) Close() error {
	close(s.closed)
	return nil
}

// repeatSSE builds an SSE stream that emits the same delta count times using
// the given event name and payload template.
func repeatSSE(event, payloadTemplate string, count int, trailer ...string) string {
	lines := []string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_1","model":"grok-4.6","status":"in_progress"}}`, "",
	}
	for i := 0; i < count; i++ {
		lines = append(lines, "event: "+event, "data: "+payloadTemplate, "")
	}
	lines = append(lines, trailer...)
	lines = append(lines,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`, "", "")
	return strings.Join(lines, "\n")
}

// A visible-content loop is a real quota burn: it must be terminated near the
// content threshold and must not be allowed to run to the reasoning threshold.
func TestConvertResponsesStreamTerminatesContentDoomLoop(t *testing.T) {
	stream := repeatSSE("response.output_text.delta",
		`{"type":"response.output_text.delta","delta":"loop"}`,
		contentDoomLoopTotal+8)
	_, err := io.ReadAll(ConvertResponseStream(io.NopCloser(strings.NewReader(stream)), OperationChat))
	if err == nil {
		t.Fatal("repeated visible content must terminate the stream")
	}
	if !errors.Is(err, neterror.ErrUpstreamOutputLoop) {
		t.Fatalf("content loop must wrap ErrUpstreamOutputLoop: %v", err)
	}
	if !strings.Contains(err.Error(), "model output loop detected") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConvertResponsesStreamAllowsExactlyContentThreshold(t *testing.T) {
	stream := repeatSSE("response.output_text.delta",
		`{"type":"response.output_text.delta","delta":"loop"}`,
		contentDoomLoopTotal-1)
	converted, err := io.ReadAll(ConvertResponseStream(io.NopCloser(strings.NewReader(stream)), OperationChat))
	if err != nil {
		t.Fatalf("the content ceiling itself must remain valid: %v", err)
	}
	if !strings.Contains(string(converted), "data: [DONE]") {
		t.Fatalf("stream did not complete: %s", converted)
	}
}

func TestConvertResponsesStreamProtectsDeferredWebSearchText(t *testing.T) {
	stream := repeatSSE("response.output_text.delta",
		`{"type":"response.output_text.delta","delta":"loop"}`,
		contentDoomLoopTotal+8)
	_, err := io.ReadAll(ConvertResponseStreamWithOptions(
		io.NopCloser(strings.NewReader(stream)),
		OperationMessages,
		ResponseOptions{AnthropicWebSearch: true},
	))
	if err == nil || !strings.Contains(err.Error(), "model output loop detected") {
		t.Fatalf("deferred web-search text must retain loop protection: %v", err)
	}
}

func TestConvertResponsesStreamProtectsAfterStopSequence(t *testing.T) {
	stream := repeatSSE("response.output_text.delta",
		`{"type":"response.output_text.delta","delta":"STOP"}`,
		contentDoomLoopTotal+8)
	_, err := io.ReadAll(ConvertResponseStreamWithOptions(
		io.NopCloser(strings.NewReader(stream)),
		OperationChat,
		ResponseOptions{StopSequences: []string{"STOP"}},
	))
	if err == nil || !strings.Contains(err.Error(), "model output loop detected") {
		t.Fatalf("discarded post-stop deltas must retain loop protection: %v", err)
	}
}

// 59 identical reasoning deltas remain valid; 60 trips the shared cycle guard.
func TestConvertResponsesStreamKeepsRepeatedReasoningBelowThreshold(t *testing.T) {
	for _, event := range []string{"response.reasoning_text.delta", "response.reasoning_summary_text.delta"} {
		t.Run(event, func(t *testing.T) {
			stream := repeatSSE(event,
				fmt.Sprintf(`{"type":%q,"item_id":"rs_1","delta":"hmm"}`, event),
				contentDoomLoopTotal-1,
				`event: response.output_text.delta`,
				`data: {"type":"response.output_text.delta","delta":"answer"}`, "")
			converted, err := io.ReadAll(ConvertResponseStream(io.NopCloser(strings.NewReader(stream)), OperationChat))
			if err != nil {
				t.Fatalf("59 reasoning deltas must not be treated as a loop: %v", err)
			}
			if !strings.Contains(string(converted), `"content":"answer"`) {
				t.Fatalf("visible answer was lost: %s", converted)
			}
		})
	}
}

// The elevated reasoning threshold is a higher ceiling, not an exemption:
// a runaway reasoning loop still has to be terminated.
func TestConvertResponsesStreamTerminatesReasoningDoomLoop(t *testing.T) {
	for _, testCase := range []struct {
		event string
		want  string
	}{
		{"response.reasoning_text.delta", "model reasoning loop detected"},
		{"response.reasoning_summary_text.delta", "model reasoning summary loop detected"},
	} {
		t.Run(testCase.event, func(t *testing.T) {
			stream := repeatSSE(testCase.event,
				fmt.Sprintf(`{"type":%q,"item_id":"rs_1","delta":"hmm"}`, testCase.event),
				contentDoomLoopTotal+8)
			_, err := io.ReadAll(ConvertResponseStream(io.NopCloser(strings.NewReader(stream)), OperationChat))
			if err == nil {
				t.Fatal("a runaway reasoning loop must still terminate the stream")
			}
			if !errors.Is(err, neterror.ErrUpstreamOutputLoop) {
				t.Fatalf("reasoning loop must wrap ErrUpstreamOutputLoop: %v", err)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestConvertResponsesStreamTracksSuppressedReasoning(t *testing.T) {
	stream := repeatSSE("response.reasoning_text.delta",
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","delta":"hmm"}`,
		contentDoomLoopTotal+8)
	_, err := io.ReadAll(ConvertResponseStream(io.NopCloser(strings.NewReader(stream)), OperationMessages))
	if err == nil || !strings.Contains(err.Error(), "model reasoning loop detected") {
		t.Fatalf("reasoning hidden from the downstream protocol must still be guarded: %v", err)
	}
}

func TestConvertResponsesStreamDoesNotCountFlushedSummaryTwice(t *testing.T) {
	repeats := contentDoomLoopTotal - 1
	lines := []string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_1","model":"grok-4.6","status":"in_progress"}}`, "",
	}
	for i := 0; i < repeats; i++ {
		itemID := fmt.Sprintf("rs_%d", i)
		lines = append(lines,
			`event: response.reasoning_summary_text.delta`,
			fmt.Sprintf(`data: {"type":"response.reasoning_summary_text.delta","item_id":%q,"delta":"hmm"}`, itemID), "",
			`event: response.output_item.done`,
			fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"id":%q,"type":"reasoning"}}`, itemID), "",
		)
	}
	lines = append(lines,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`, "", "")
	converted, err := io.ReadAll(ConvertResponseStream(
		io.NopCloser(strings.NewReader(strings.Join(lines, "\n"))), OperationChat,
	))
	if err != nil {
		t.Fatalf("flushing buffered summaries must not increment the upstream repeat counter: %v", err)
	}
	if !strings.Contains(string(converted), "data: [DONE]") {
		t.Fatalf("stream did not complete: %s", converted)
	}
}

func TestConvertResponsesStreamSharesReasoningCounterAcrossEventTypes(t *testing.T) {
	lines := []string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_1","model":"grok-4.6","status":"in_progress"}}`, "",
	}
	for i := 0; i < contentDoomLoopTotal/2; i++ {
		lines = append(lines,
			`event: response.reasoning_summary_text.delta`,
			`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","delta":"hmm"}`, "")
	}
	for i := 0; i <= contentDoomLoopTotal/2; i++ {
		lines = append(lines,
			`event: response.reasoning_text.delta`,
			`data: {"type":"response.reasoning_text.delta","item_id":"rs_1","delta":"hmm"}`, "")
	}
	_, err := io.ReadAll(ConvertResponseStream(
		io.NopCloser(strings.NewReader(strings.Join(lines, "\n"))), OperationChat,
	))
	if err == nil || !strings.Contains(err.Error(), "model reasoning loop detected") {
		t.Fatalf("summary and raw reasoning must share one upstream counter: %v", err)
	}
}

func TestConvertResponseStreamGuardsNativeResponsesWithoutRewriting(t *testing.T) {
	t.Run("passthrough", func(t *testing.T) {
		source := ": keep-this-comment\r\n\r\n" + repeatSSE("response.output_text.delta",
			`{"type":"response.output_text.delta","delta":"answer"}`, 1)
		converted, err := io.ReadAll(ConvertResponseStream(
			io.NopCloser(strings.NewReader(source)), OperationResponses,
		))
		if err != nil {
			t.Fatalf("native response passthrough failed: %v", err)
		}
		if string(converted) != source {
			t.Fatalf("native response bytes changed:\nwant %q\n got %q", source, converted)
		}
	})

	t.Run("doom loop", func(t *testing.T) {
		stream := repeatSSE("response.output_text.delta",
			`{"type":"response.output_text.delta","delta":"loop"}`,
			contentDoomLoopTotal+8)
		_, err := io.ReadAll(ConvertResponseStream(
			io.NopCloser(strings.NewReader(stream)), OperationResponses,
		))
		if err == nil || !strings.Contains(err.Error(), "model output loop detected") {
			t.Fatalf("native responses must retain loop protection: %v", err)
		}
	})
}

func TestConvertResponseStreamCloseImmediatelyClosesUpstream(t *testing.T) {
	for _, operation := range []string{OperationChat, OperationResponses} {
		t.Run(operation, func(t *testing.T) {
			source := &blockingStreamSource{closed: make(chan struct{})}
			stream := ConvertResponseStream(source, operation)
			if err := stream.Close(); err != nil {
				t.Fatalf("close converted stream: %v", err)
			}
			select {
			case <-source.closed:
			default:
				t.Fatal("closing the downstream stream did not close the upstream source")
			}
		})
	}
}

// A 1-item cycle of 59 identical formatting deltas is still allowed; 60 trips
// the content loop guard.
func TestConvertResponsesStreamKeepsMarkdownRuleAndTableBorders(t *testing.T) {
	for _, testCase := range []struct{ name, delta string }{
		{"horizontal rule", "-"},
		{"table border", "="},
		{"empty table cells", " | "},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stream := repeatSSE("response.output_text.delta",
				fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, testCase.delta),
				contentDoomLoopTotal-1)
			converted, err := io.ReadAll(ConvertResponseStream(io.NopCloser(strings.NewReader(stream)), OperationChat))
			if err != nil {
				t.Fatalf("legitimate repeated formatting must not be treated as a loop: %v", err)
			}
			if !strings.Contains(string(converted), "data: [DONE]") {
				t.Fatalf("stream did not complete: %s", converted)
			}
		})
	}
}

// Interleaved reasoning with distinct content must not share a counter.
func TestConvertResponsesStreamDoomLoopCountersResetAndStaySeparate(t *testing.T) {
	lines := []string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_1","model":"grok-4.6","status":"in_progress"}}`, "",
	}
	for i := 0; i < contentDoomLoopTotal-1; i++ {
		delta := "a"
		if i%2 == 1 {
			delta = "b"
		}
		lines = append(lines,
			`event: response.output_text.delta`,
			fmt.Sprintf(`data: {"type":"response.output_text.delta","delta":%q}`, delta), "")
	}
	for i := 0; i < contentDoomLoopTotal-1; i++ {
		lines = append(lines,
			`event: response.reasoning_text.delta`,
			`data: {"type":"response.reasoning_text.delta","item_id":"rs_1","delta":"hmm"}`, "",
			`event: response.output_text.delta`,
			fmt.Sprintf(`data: {"type":"response.output_text.delta","delta":"tick%d"}`, i), "")
	}
	lines = append(lines,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`, "", "")
	stream := strings.Join(lines, "\n")
	converted, err := io.ReadAll(ConvertResponseStream(io.NopCloser(strings.NewReader(stream)), OperationChat))
	if err != nil {
		t.Fatalf("alternating deltas must not be treated as a loop: %v", err)
	}
	if !strings.Contains(string(converted), "data: [DONE]") {
		t.Fatalf("stream did not complete: %s", converted)
	}
}

func tileDeltas(pattern []string, n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = pattern[i%len(pattern)]
	}
	return out
}

func feedContent(deltas []string) error {
	var tracker streamRepeatTracker
	for _, delta := range deltas {
		if err := tracker.trackContent(delta); err != nil {
			return err
		}
	}
	return nil
}

func TestContentCycleLoopDetectsOneToThreePeriod(t *testing.T) {
	tests := []struct {
		name    string
		deltas  []string
		wantErr bool
	}{
		{name: "59 identical allowed", deltas: tileDeltas([]string{"loop"}, 59)},
		{name: "60 identical is a loop", deltas: tileDeltas([]string{"loop"}, 60), wantErr: true},
		{name: "59 ab allowed", deltas: tileDeltas([]string{" \n", " \n\n"}, 59)},
		{name: "60 ab is a loop", deltas: tileDeltas([]string{" \n", " \n\n"}, 60), wantErr: true},
		{name: "60 abc is a loop", deltas: tileDeltas([]string{"a", "b", "c"}, 60), wantErr: true},
		{name: "60 abcd is not a 1-3 cycle", deltas: tileDeltas([]string{"a", "b", "c", "d"}, 60)},
		{name: "real tokens then 60 ab", deltas: append([]string{"界面", "和", "引擎", "都", "改", "好"}, tileDeltas([]string{" \n", " \n\n"}, 60)...), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := feedContent(test.deltas)
			if test.wantErr {
				if err == nil || !errors.Is(err, neterror.ErrUpstreamOutputLoop) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err = %v", err)
			}
		})
	}
}

func feedReasoning(deltas []string) error {
	var tracker streamRepeatTracker
	for _, delta := range deltas {
		if err := tracker.trackReasoning(delta, "model reasoning loop detected"); err != nil {
			return err
		}
	}
	return nil
}

func TestReasoningCycleLoopDetectsOneToThreePeriod(t *testing.T) {
	tests := []struct {
		name    string
		deltas  []string
		wantErr bool
	}{
		{name: "59 identical allowed", deltas: tileDeltas([]string{"hmm"}, 59)},
		{name: "60 identical is a loop", deltas: tileDeltas([]string{"hmm"}, 60), wantErr: true},
		{name: "59 ab allowed", deltas: tileDeltas([]string{"so", "wait"}, 59)},
		{name: "60 ab is a loop", deltas: tileDeltas([]string{"so", "wait"}, 60), wantErr: true},
		{name: "60 abc is a loop", deltas: tileDeltas([]string{"so", "hmm", "wait"}, 60), wantErr: true},
		{name: "60 abcd is not a 1-3 cycle", deltas: tileDeltas([]string{"a", "b", "c", "d"}, 60)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := feedReasoning(test.deltas)
			if test.wantErr {
				if err == nil || !errors.Is(err, neterror.ErrUpstreamOutputLoop) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err = %v", err)
			}
		})
	}
}

func TestConvertResponsesStreamTerminatesAlternatingReasoningLoop(t *testing.T) {
	for _, event := range []string{"response.reasoning_text.delta", "response.reasoning_summary_text.delta"} {
		t.Run(event, func(t *testing.T) {
			lines := []string{
				`event: response.created`,
				`data: {"type":"response.created","response":{"id":"resp_1","model":"grok-4.6","status":"in_progress"}}`, "",
			}
			for i := 0; i < contentDoomLoopTotal; i++ {
				delta := "so"
				if i%2 == 1 {
					delta = "wait"
				}
				lines = append(lines,
					"event: "+event,
					fmt.Sprintf(`data: {"type":%q,"item_id":"rs_1","delta":%q}`, event, delta), "")
			}
			_, err := io.ReadAll(ConvertResponseStream(io.NopCloser(strings.NewReader(strings.Join(lines, "\n"))), OperationChat))
			if err == nil || !errors.Is(err, neterror.ErrUpstreamOutputLoop) {
				t.Fatalf("alternating reasoning must terminate: %v", err)
			}
		})
	}
}

func TestConvertResponsesStreamTerminatesAlternatingWhitespaceLoop(t *testing.T) {
	lines := []string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_1","model":"grok-4.6","status":"in_progress"}}`, "",
	}
	for i := 0; i < contentDoomLoopTotal; i++ {
		delta := " \n"
		if i%2 == 1 {
			delta = " \n\n"
		}
		lines = append(lines,
			`event: response.output_text.delta`,
			fmt.Sprintf(`data: {"type":"response.output_text.delta","delta":%q}`, delta), "")
	}
	stream := strings.Join(lines, "\n")
	_, err := io.ReadAll(ConvertResponseStream(io.NopCloser(strings.NewReader(stream)), OperationChat))
	if err == nil || !errors.Is(err, neterror.ErrUpstreamOutputLoop) {
		t.Fatalf("alternating whitespace must terminate: %v", err)
	}
}
