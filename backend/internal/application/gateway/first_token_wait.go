package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/audit"
	"github.com/chenyme/grok2api/backend/internal/pkg/neterror"
)

// firstTokenStreamKind selects which SSE payload shape counts as the first
// content token (matches inference handler / audit 首字, including reasoning).
type firstTokenStreamKind int

const (
	firstTokenKindResponses firstTokenStreamKind = iota
	firstTokenKindChat
	firstTokenKindAnthropic
)

func firstTokenKindForOperation(operation audit.Operation) firstTokenStreamKind {
	switch operation {
	case audit.OperationChat:
		return firstTokenKindChat
	case audit.OperationMessages:
		return firstTokenKindAnthropic
	default:
		return firstTokenKindResponses
	}
}

// awaitFirstContentToken reads body until the first generated content delta
// (or deadline). Unlike transport first-byte idle, metadata SSE events do NOT
// clear the deadline — only a real content/reasoning delta does.
//
// On success, returns the buffered prefix plus the remaining body so the caller
// can hand off a continuous stream. On timeout, closes body and returns
// ErrUpstreamFirstCharTimeout so the attempt loop can rotate accounts before
// any client headers are written.
func awaitFirstContentToken(body io.ReadCloser, deadline time.Duration, kind firstTokenStreamKind) (prefix []byte, rest io.ReadCloser, err error) {
	if body == nil {
		return nil, nil, errors.New("nil response body")
	}
	if deadline <= 0 {
		return nil, body, nil
	}

	var timedOut atomic.Bool
	timer := time.AfterFunc(deadline, func() {
		if timedOut.CompareAndSwap(false, true) {
			_ = body.Close()
		}
	})
	stopTimer := func() {
		timer.Stop()
	}

	var buf bytes.Buffer
	pending := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		n, readErr := body.Read(chunk)
		if timedOut.Load() {
			stopTimer()
			return nil, nil, neterror.ErrUpstreamFirstCharTimeout
		}
		if n > 0 {
			buf.Write(chunk[:n])
			pending = append(pending, chunk[:n]...)
			for {
				index := bytes.IndexByte(pending, '\n')
				if index < 0 {
					if len(pending) > 1<<20 {
						pending = nil
					}
					break
				}
				line := bytes.TrimSpace(pending[:index])
				pending = pending[index+1:]
				if !bytes.HasPrefix(line, []byte("data:")) {
					continue
				}
				data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
					continue
				}
				if isGeneratedContentDelta(data, kind) {
					stopTimer()
					return buf.Bytes(), body, nil
				}
				if isEmptyOutputCapacityPayload(data) {
					stopTimer()
					_ = body.Close()
					return nil, nil, errUpstreamModelAtCapacity
				}
			}
		}
		if readErr == nil {
			continue
		}
		if timedOut.Load() || neterror.IsUpstreamFirstCharTimeout(readErr) {
			stopTimer()
			return nil, nil, neterror.ErrUpstreamFirstCharTimeout
		}
		if errors.Is(readErr, io.EOF) {
			// Stream ended without a content token: hand off what we have.
			stopTimer()
			return buf.Bytes(), io.NopCloser(bytes.NewReader(nil)), nil
		}
		stopTimer()
		_ = body.Close()
		return nil, nil, readErr
	}
}

func isGeneratedContentDelta(data []byte, kind firstTokenStreamKind) bool {
	switch kind {
	case firstTokenKindResponses:
		var event struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if json.Unmarshal(data, &event) != nil || event.Delta == "" {
			return false
		}
		switch event.Type {
		case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta",
			"response.refusal.delta", "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
			return true
		}
	case firstTokenKindChat:
		var event struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					Reasoning        string `json:"reasoning"`
					ReasoningContent string `json:"reasoning_content"`
					Refusal          string `json:"refusal"`
					ToolCalls        []struct {
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal(data, &event) != nil {
			return false
		}
		for _, choice := range event.Choices {
			delta := choice.Delta
			if delta.Content != "" || delta.Reasoning != "" || delta.ReasoningContent != "" || delta.Refusal != "" {
				return true
			}
			for _, call := range delta.ToolCalls {
				if call.Function.Arguments != "" {
					return true
				}
			}
		}
	case firstTokenKindAnthropic:
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if json.Unmarshal(data, &event) != nil || event.Type != "content_block_delta" {
			return false
		}
		switch event.Delta.Type {
		case "text_delta":
			return event.Delta.Text != ""
		case "thinking_delta":
			return event.Delta.Thinking != ""
		case "input_json_delta":
			return event.Delta.PartialJSON != ""
		}
	}
	return false
}

// prefixThenRest streams a pre-buffered prefix followed by the remaining body.
type prefixThenRest struct {
	prefix *bytes.Reader
	rest   io.ReadCloser
}

func newPrefixThenRest(prefix []byte, rest io.ReadCloser) io.ReadCloser {
	if rest == nil {
		rest = io.NopCloser(bytes.NewReader(nil))
	}
	return &prefixThenRest{prefix: bytes.NewReader(prefix), rest: rest}
}

func (p *prefixThenRest) Read(buffer []byte) (int, error) {
	if p.prefix != nil && p.prefix.Len() > 0 {
		n, err := p.prefix.Read(buffer)
		if n > 0 {
			return n, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
	}
	p.prefix = nil
	return p.rest.Read(buffer)
}

func (p *prefixThenRest) Close() error {
	p.prefix = nil
	if p.rest == nil {
		return nil
	}
	return p.rest.Close()
}
