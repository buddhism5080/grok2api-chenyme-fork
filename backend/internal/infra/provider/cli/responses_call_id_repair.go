package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

var callIDPairFamilies = [][2]string{
	{"function_call", "function_call_output"},
	{"custom_tool_call", "custom_tool_call_output"},
	{"apply_patch_call", "apply_patch_call_output"},
	{"local_shell_call", "local_shell_call_output"},
	{"shell_call", "shell_call_output"},
	{"tool_search_call", "tool_search_output"},
}

type callIDIndexedItem struct {
	index int
	item  map[string]any
}

func historyItemType(item map[string]any) string {
	itemType := strings.TrimSpace(stringField(item, "type"))
	if itemType == "" && strings.TrimSpace(stringField(item, "role")) != "" {
		return "message"
	}
	return itemType
}

func trimCallID(item map[string]any) string {
	return strings.TrimSpace(stringField(item, "call_id"))
}

func firstNonEmptyCallID(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func inventCallID(index int, item map[string]any, kind string) string {
	payload, err := json.Marshal(struct {
		Index int            `json:"i"`
		Kind  string         `json:"k"`
		Item  map[string]any `json:"item"`
	}{Index: index, Kind: kind, Item: item})
	if err != nil {
		payload = []byte(fmt.Sprintf("%s|%d", kind, index))
	}
	sum := sha256.Sum256(payload)
	return "call_g2a_" + hex.EncodeToString(sum[:8])
}

func assignPairCallID(call, output map[string]any, index int, kind, callID string) string {
	if callID == "" {
		callID = firstNonEmptyCallID(
			trimCallID(call),
			trimCallID(output),
			strings.TrimSpace(stringField(call, "id")),
			strings.TrimSpace(stringField(output, "id")),
		)
	}
	if callID == "" {
		callID = inventCallID(index, call, kind)
	}
	call["call_id"] = callID
	output["call_id"] = callID
	return callID
}

func unusedEmpty(items []callIDIndexedItem, used []bool) []int {
	rest := make([]int, 0)
	for i, item := range items {
		if used[i] {
			continue
		}
		if trimCallID(item.item) == "" {
			rest = append(rest, i)
		}
	}
	return rest
}

func unusedNonEmpty(items []callIDIndexedItem, used []bool) []int {
	rest := make([]int, 0)
	for i, item := range items {
		if used[i] {
			continue
		}
		if trimCallID(item.item) != "" {
			rest = append(rest, i)
		}
	}
	return rest
}

func (c *responsesToolCompatibility) repairEmptyCallIDs(items []any) []any {
	if c == nil || len(items) == 0 {
		return items
	}
	drop := make(map[int]struct{})
	paired := false
	omitted := false
	for _, family := range callIDPairFamilies {
		var calls, outputs []callIDIndexedItem
		for index, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch historyItemType(item) {
			case family[0]:
				calls = append(calls, callIDIndexedItem{index: index, item: item})
			case family[1]:
				outputs = append(outputs, callIDIndexedItem{index: index, item: item})
			}
		}
		usedCall := make([]bool, len(calls))
		usedOut := make([]bool, len(outputs))
		for ci, call := range calls {
			cid := trimCallID(call.item)
			if cid == "" {
				continue
			}
			for oi, output := range outputs {
				if usedOut[oi] {
					continue
				}
				if trimCallID(output.item) == cid {
					usedCall[ci] = true
					usedOut[oi] = true
					break
				}
			}
		}
		emptyCalls := unusedEmpty(calls, usedCall)
		emptyOuts := unusedEmpty(outputs, usedOut)
		n := min(len(emptyCalls), len(emptyOuts))
		for k := 0; k < n; k++ {
			call := calls[emptyCalls[k]]
			output := outputs[emptyOuts[k]]
			assignPairCallID(call.item, output.item, call.index, family[0], "")
			usedCall[emptyCalls[k]] = true
			usedOut[emptyOuts[k]] = true
			paired = true
		}
		emptyCalls = unusedEmpty(calls, usedCall)
		namedOuts := unusedNonEmpty(outputs, usedOut)
		n = min(len(emptyCalls), len(namedOuts))
		for k := 0; k < n; k++ {
			call := calls[emptyCalls[k]]
			output := outputs[namedOuts[k]]
			assignPairCallID(call.item, output.item, call.index, family[0], trimCallID(output.item))
			usedCall[emptyCalls[k]] = true
			usedOut[namedOuts[k]] = true
			paired = true
		}
		emptyOuts = unusedEmpty(outputs, usedOut)
		namedCalls := unusedNonEmpty(calls, usedCall)
		n = min(len(emptyOuts), len(namedCalls))
		for k := 0; k < n; k++ {
			call := calls[namedCalls[k]]
			output := outputs[emptyOuts[k]]
			assignPairCallID(call.item, output.item, call.index, family[0], trimCallID(call.item))
			usedCall[namedCalls[k]] = true
			usedOut[emptyOuts[k]] = true
			paired = true
		}
		for i, used := range usedCall {
			if used || trimCallID(calls[i].item) != "" {
				continue
			}
			drop[calls[i].index] = struct{}{}
			omitted = true
		}
		for i, used := range usedOut {
			if used || trimCallID(outputs[i].item) != "" {
				continue
			}
			drop[outputs[i].index] = struct{}{}
			omitted = true
		}
	}
	if paired {
		c.changed = true
		c.addWarning("empty_call_id_paired")
	}
	if omitted {
		c.changed = true
		c.addWarning("empty_call_id_orphan_omitted")
	}
	if len(drop) == 0 {
		return items
	}
	kept := make([]any, 0, len(items)-len(drop))
	for index, raw := range items {
		if _, skip := drop[index]; skip {
			continue
		}
		kept = append(kept, raw)
	}
	return kept
}
