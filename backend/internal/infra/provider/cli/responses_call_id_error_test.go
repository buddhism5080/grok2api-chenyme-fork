package cli

import "testing"

func TestEmptyCallIDErrorIncludesType(t *testing.T) {
	err := emptyCallIDError("input[668]", "function_call_output")
	if err == nil || err.Message != "input[668] (function_call_output).call_id 不能为空" || err.Param != "input[668].call_id" || err.Code != "invalid_parameter" {
		t.Fatalf("%#v", err)
	}
}
