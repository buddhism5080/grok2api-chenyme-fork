package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

var errUpstreamModelAtCapacity = errors.New("upstream model at capacity")

const emptyCapacityNeedle = "currently at capacity due to high demand"

// peekEmptyCapacityStream reads a Build SSE body until it can tell whether the
// model produced any output. Keepalives and metadata do not count.
//
// A capacity error with no generated delta is returned as
// errUpstreamModelAtCapacity so the attempt loop can rotate accounts before
// any client headers are written. Other terminal SSE errors are handed off.
func peekEmptyCapacityStream(body io.ReadCloser) (prefix []byte, rest io.ReadCloser, err error) {
	if body == nil {
		return nil, nil, errors.New("nil response body")
	}
	var buf bytes.Buffer
	pending := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		n, readErr := body.Read(chunk)
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
				if streamEventHasGeneratedOutput(data) {
					return buf.Bytes(), body, nil
				}
				if isEmptyOutputCapacityPayload(data) {
					_ = body.Close()
					return nil, nil, errUpstreamModelAtCapacity
				}
			}
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			_ = body.Close()
			return buf.Bytes(), io.NopCloser(bytes.NewReader(nil)), nil
		}
		_ = body.Close()
		return nil, nil, readErr
	}
}

func isEmptyOutputCapacityPayload(data []byte) bool {
	return bytes.Contains(bytes.ToLower(data), []byte(emptyCapacityNeedle))
}

func streamEventHasGeneratedOutput(data []byte) bool {
	var responses struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	}
	if json.Unmarshal(data, &responses) == nil && responses.Delta != "" {
		switch responses.Type {
		case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta",
			"response.refusal.delta", "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
			return true
		}
	}
	var chat struct {
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
	if json.Unmarshal(data, &chat) == nil {
		for _, choice := range chat.Choices {
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
	}
	var anthropic struct {
		Type  string `json:"type"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if json.Unmarshal(data, &anthropic) == nil && anthropic.Type == "content_block_delta" {
		switch anthropic.Delta.Type {
		case "text_delta":
			return anthropic.Delta.Text != ""
		case "thinking_delta":
			return anthropic.Delta.Thinking != ""
		case "input_json_delta":
			return anthropic.Delta.PartialJSON != ""
		}
	}
	return false
}

type emptyCapacityReplay struct {
	prefix *bytes.Reader
	rest   io.ReadCloser
}

func newEmptyCapacityReplay(prefix []byte, rest io.ReadCloser) io.ReadCloser {
	if rest == nil {
		rest = io.NopCloser(bytes.NewReader(nil))
	}
	return &emptyCapacityReplay{prefix: bytes.NewReader(prefix), rest: rest}
}

func (p *emptyCapacityReplay) Read(buffer []byte) (int, error) {
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

func (p *emptyCapacityReplay) Close() error {
	p.prefix = nil
	if p.rest == nil {
		return nil
	}
	return p.rest.Close()
}
