package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeResponsesRequestCopiesCallIDFromPairedOutput(t *testing.T) {
	items, compatibility := normalizeResponsesInput(t, `[
		{"type":"message","role":"user","content":"hi"},
		{"type":"function_call","name":"lookup","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_from_out","output":"ok"}
	]`)
	if len(items) != 3 {
		t.Fatalf("input = %#v", items)
	}
	call := items[1].(map[string]any)
	output := items[2].(map[string]any)
	if call["call_id"] != "call_from_out" || output["call_id"] != "call_from_out" {
		t.Fatalf("call=%#v output=%#v", call, output)
	}
	if compatibility == nil || !strings.Contains(compatibility.warningHeader(), "empty_call_id_paired") {
		t.Fatalf("warnings = %q", compatibility.warningHeader())
	}
}

func TestNormalizeResponsesRequestCopiesCallIDFromPairedCall(t *testing.T) {
	items, _ := normalizeResponsesInput(t, `[
		{"type":"function_call","call_id":"call_from_call","name":"lookup","arguments":"{}"},
		{"type":"function_call_output","output":"ok"}
	]`)
	if items[0].(map[string]any)["call_id"] != "call_from_call" || items[1].(map[string]any)["call_id"] != "call_from_call" {
		t.Fatalf("input = %#v", items)
	}
}

func TestNormalizeResponsesRequestUsesItemIDWhenBothCallIDsEmpty(t *testing.T) {
	items, _ := normalizeResponsesInput(t, `[
		{"type":"function_call","id":"fc_abc","name":"lookup","arguments":"{}"},
		{"type":"function_call_output","output":"ok"}
	]`)
	if items[0].(map[string]any)["call_id"] != "fc_abc" || items[1].(map[string]any)["call_id"] != "fc_abc" {
		t.Fatalf("input = %#v", items)
	}
}

func TestNormalizeResponsesRequestInventsStableCallIDWhenPairHasNone(t *testing.T) {
	body := []byte(`{"model":"public","input":[
		{"type":"function_call","name":"lookup","arguments":"{\"q\":1}"},
		{"type":"function_call_output","output":"ok"}
	]}`)
	first, compatibility, err := normalizeResponsesRequest(body, "grok-4.5")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := normalizeResponsesRequest(body, "grok-4.5")
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	if err := json.Unmarshal(first, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second, &b); err != nil {
		t.Fatal(err)
	}
	callID := a["input"].([]any)[0].(map[string]any)["call_id"].(string)
	if callID == "" || !strings.HasPrefix(callID, "call_g2a_") {
		t.Fatalf("invented call_id = %q", callID)
	}
	if a["input"].([]any)[1].(map[string]any)["call_id"] != callID {
		t.Fatalf("pair call_id mismatch: %#v", a["input"])
	}
	if b["input"].([]any)[0].(map[string]any)["call_id"] != callID {
		t.Fatalf("invented call_id was not stable: first=%q second=%v", callID, b["input"].([]any)[0])
	}
	if compatibility == nil || !strings.Contains(compatibility.warningHeader(), "empty_call_id_paired") {
		t.Fatalf("warnings = %q", compatibility.warningHeader())
	}
}

func TestNormalizeResponsesRequestDropsEmptyCallIDOrphanOutputOnly(t *testing.T) {
	items, compatibility := normalizeResponsesInput(t, `[
		{"type":"message","role":"user","content":"hi"},
		{"type":"function_call_output","output":"orphan"}
	]`)
	if len(items) != 1 || items[0].(map[string]any)["content"] != "hi" {
		t.Fatalf("expected only user message, got %#v", items)
	}
	if compatibility == nil || !strings.Contains(compatibility.warningHeader(), "empty_call_id_orphan_omitted") {
		t.Fatalf("warnings = %q", compatibility.warningHeader())
	}
}

func TestNormalizeResponsesRequestDropsEmptyCallIDOrphanCallOnly(t *testing.T) {
	items, compatibility := normalizeResponsesInput(t, `[
		{"type":"message","role":"user","content":"hi"},
		{"type":"function_call","call_id":"   ","name":"lookup","arguments":"{}"}
	]`)
	if len(items) != 1 || items[0].(map[string]any)["content"] != "hi" {
		t.Fatalf("input = %#v", items)
	}
	if !strings.Contains(compatibility.warningHeader(), "empty_call_id_orphan_omitted") {
		t.Fatalf("warnings = %q", compatibility.warningHeader())
	}
}

func TestNormalizeResponsesRequestKeepsInProgressCallWithCallID(t *testing.T) {
	items, _ := normalizeResponsesInput(t, `[
		{"type":"message","role":"user","content":"hi"},
		{"type":"function_call","call_id":"call_live","name":"lookup","arguments":"{}"}
	]`)
	if len(items) != 2 || items[1].(map[string]any)["call_id"] != "call_live" {
		t.Fatalf("input = %#v", items)
	}
}

func TestNormalizeResponsesRequestPairsByCallIDThenFIFOEmpties(t *testing.T) {
	items, _ := normalizeResponsesInput(t, `[
		{"type":"function_call","call_id":"call_a","name":"a","arguments":"{}"},
		{"type":"function_call","name":"b","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_a","output":"oa"},
		{"type":"function_call_output","output":"ob"}
	]`)
	if len(items) != 4 {
		t.Fatalf("input = %#v", items)
	}
	if items[0].(map[string]any)["call_id"] != "call_a" || items[2].(map[string]any)["call_id"] != "call_a" {
		t.Fatalf("named pair = %#v", items)
	}
	emptyCallID := items[1].(map[string]any)["call_id"].(string)
	if emptyCallID == "" || emptyCallID == "call_a" || items[3].(map[string]any)["call_id"] != emptyCallID {
		t.Fatalf("fifo empty pair = %#v", items)
	}
}

func TestNormalizeResponsesRequestPairsAcrossReasoning(t *testing.T) {
	items, _ := normalizeResponsesInput(t, `[
		{"type":"function_call","name":"lookup","arguments":"{}"},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"plan"}]},
		{"type":"function_call_output","call_id":"call_across","output":"ok"}
	]`)
	if items[0].(map[string]any)["call_id"] != "call_across" || items[2].(map[string]any)["call_id"] != "call_across" {
		t.Fatalf("input = %#v", items)
	}
}

func TestNormalizeResponsesRequestDoesNotPairAcrossFamilies(t *testing.T) {
	items, compatibility := normalizeResponsesInput(t, `[
		{"type":"message","role":"user","content":"hi"},
		{"type":"function_call","name":"lookup","arguments":"{}"},
		{"type":"shell_call_output","call_id":"call_shell","output":[{"stdout":"x","stderr":"","outcome":{"type":"exit","exit_code":0}}]}
	]`)
	if len(items) != 2 {
		t.Fatalf("input = %#v", items)
	}
	if items[1].(map[string]any)["type"] != "shell_call_output" || items[1].(map[string]any)["call_id"] != "call_shell" {
		t.Fatalf("kept shell output = %#v", items[1])
	}
	if !strings.Contains(compatibility.warningHeader(), "empty_call_id_orphan_omitted") {
		t.Fatalf("warnings = %q", compatibility.warningHeader())
	}
}

func TestNormalizeResponsesRequestRepairsShellAndApplyPatchPairs(t *testing.T) {
	items, _ := normalizeResponsesInput(t, `[
		{"type":"shell_call","action":{"commands":["pwd"]}},
		{"type":"shell_call_output","call_id":"call_sh","output":[{"stdout":"/tmp\n","stderr":"","outcome":{"type":"exit","exit_code":0}}]},
		{"type":"apply_patch_call","operation":{"type":"delete_file","path":"old.txt"}},
		{"type":"apply_patch_call_output","call_id":"call_ap","output":"done"},
		{"type":"local_shell_call","action":{"type":"exec","command":["pwd"]}},
		{"type":"local_shell_call_output","call_id":"call_ls","output":"ok"}
	]`)
	if len(items) != 6 {
		t.Fatalf("input = %#v", items)
	}
	if items[0].(map[string]any)["call_id"] != "call_sh" || items[1].(map[string]any)["call_id"] != "call_sh" {
		t.Fatalf("shell pair = %#v", items[0:2])
	}
	if items[2].(map[string]any)["call_id"] != "call_ap" || items[3].(map[string]any)["call_id"] != "call_ap" {
		t.Fatalf("apply_patch pair = %#v", items[2:4])
	}
	if items[4].(map[string]any)["call_id"] != "call_ls" || items[5].(map[string]any)["call_id"] != "call_ls" {
		t.Fatalf("local_shell pair = %#v", items[4:6])
	}
}

func normalizeResponsesInput(t *testing.T, inputJSON string) ([]any, *responsesToolCompatibility) {
	t.Helper()
	normalized, compatibility, err := normalizeResponsesRequest([]byte(`{"model":"public","input":`+inputJSON+`}`), "grok-4.5")
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal(normalized, &request); err != nil {
		t.Fatal(err)
	}
	items, _ := request["input"].([]any)
	return items, compatibility
}
