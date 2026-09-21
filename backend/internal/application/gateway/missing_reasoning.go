package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
)

const (
	defaultMissingReasoningPenaltyTTL = 24 * time.Hour
	missingReasoningOutputTokenMin    = 20
)

type accountPenaltyBook struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[uint64]time.Time
}

func newAccountPenaltyBook(ttl time.Duration) *accountPenaltyBook {
	if ttl <= 0 {
		ttl = defaultMissingReasoningPenaltyTTL
	}
	return &accountPenaltyBook{ttl: ttl, entries: make(map[uint64]time.Time)}
}

func (b *accountPenaltyBook) Penalized(accountID uint64, now time.Time) bool {
	if b == nil || accountID == 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.entries[accountID]
	if !ok || until.IsZero() || !now.Before(until) {
		if ok {
			delete(b.entries, accountID)
		}
		return false
	}
	return true
}

func (b *accountPenaltyBook) Latch(accountID uint64, now time.Time) bool {
	if b == nil || accountID == 0 {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.entries[accountID]
	if ok && now.Before(until) {
		return false
	}
	b.entries[accountID] = now.Add(b.ttl)
	return true
}

func missingReasoningPenaltyApplies(provider account.Provider, statusCode int, outputTokens, reasoningTokens int64, publicModel string, enabled bool, models map[string]struct{}) bool {
	if !enabled || len(models) == 0 {
		return false
	}
	if provider != account.ProviderBuild || statusCode != http.StatusOK {
		return false
	}
	if outputTokens <= missingReasoningOutputTokenMin || reasoningTokens != 0 {
		return false
	}
	model := strings.ToLower(strings.TrimSpace(publicModel))
	if model == "" {
		return false
	}
	_, ok := models[model]
	return ok
}

func (s *Selector) schedulingPenalized(accountID uint64, now time.Time) bool {
	if s == nil {
		return false
	}
	if s.usagePenalty != nil && s.usagePenalty.Penalized(accountID, now) {
		return true
	}
	if s.missingReasoningPenalty != nil && s.missingReasoningPenalty.Penalized(accountID, now) {
		return true
	}
	return false
}

func (s *Selector) RecordMissingReasoningPenalty(accountID uint64, now time.Time) {
	if s == nil || accountID == 0 {
		return
	}
	if s.missingReasoningPenalty == nil {
		s.missingReasoningPenalty = newAccountPenaltyBook(0)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !s.missingReasoningPenalty.Latch(accountID, now) {
		return
	}
	if s.sticky != nil {
		_ = s.sticky.DeleteByAccount(context.Background(), accountID)
	}
}

func missingReasoningUserTurnGateBlocks(modelID string, userTurnModels map[string]struct{}, body []byte) bool {
	if len(userTurnModels) == 0 {
		return false
	}
	if _, ok := userTurnModels[modelID]; !ok {
		return false
	}
	return !lastConversationTurnIsUserPrompt(body)
}

// lastConversationTurnIsUserPrompt reports whether the last conversation item is a
// genuine user prompt. Tool-result turns (function_call_output, role=tool,
// Anthropic tool_result-only user messages) return false. Unparseable bodies
// also return false so the user-turn gate fails closed.
func lastConversationTurnIsUserPrompt(body []byte) bool {
	if len(bytes.TrimSpace(body)) == 0 {
		return false
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	if raw, ok := payload["input"]; ok && !jsonRawEmpty(raw) {
		return lastJSONTurnIsUserPrompt(raw)
	}
	if raw, ok := payload["messages"]; ok && !jsonRawEmpty(raw) {
		return lastJSONTurnIsUserPrompt(raw)
	}
	return false
}

func lastJSONTurnIsUserPrompt(raw json.RawMessage) bool {
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return strings.TrimSpace(asString) != ""
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return false
	}
	return conversationItemIsUserPrompt(items[len(items)-1])
}

func conversationItemIsUserPrompt(raw json.RawMessage) bool {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil {
		return false
	}
	kind := jsonRawString(item["type"])
	switch kind {
	case "function_call_output", "custom_tool_call_output", "tool_result",
		"computer_call_output", "local_shell_call_output", "shell_call_output",
		"mcp_call", "mcp_list_tools", "mcp_approval_response",
		"function_call", "custom_tool_call", "computer_call", "local_shell_call",
		"shell_call", "reasoning", "item_reference":
		return false
	}
	role := jsonRawString(item["role"])
	switch role {
	case "tool", "function", "assistant", "system", "developer":
		return false
	}
	if role == "user" || role == "" || kind == "message" || kind == "" {
		return !contentIsOnlyToolResult(item["content"])
	}
	return false
}

func contentIsOnlyToolResult(raw json.RawMessage) bool {
	if jsonRawEmpty(raw) {
		return false
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return false
	}
	var parts []map[string]any
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		kind, _ := part["type"].(string)
		if strings.ToLower(strings.TrimSpace(kind)) != "tool_result" {
			return false
		}
	}
	return true
}

func jsonRawEmpty(raw json.RawMessage) bool {
	value := bytes.TrimSpace(raw)
	return len(value) == 0 || bytes.Equal(value, []byte("null"))
}

func jsonRawString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(value))
}
