package gateway

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/domain/audit"
)

func TestSessionRotationBookKeepsStableIDUntilNextPenalty(t *testing.T) {
	now := time.Now().UTC()
	book := newSessionRotationBook(time.Hour, 2)
	if book.outboundID("base", now) != "" {
		t.Fatal("unpenalized session must keep its original id")
	}
	first := book.bump("base", now)
	secondRead := book.outboundID("base", now.Add(time.Minute))
	if first == "" || first == "base" || secondRead != first {
		t.Fatalf("stable rotation = %q then %q", first, secondRead)
	}
	next := book.bump("base", now.Add(2*time.Minute))
	if next == "" || next == first {
		t.Fatalf("second penalty reused %q", next)
	}
	book.bump("other", now.Add(3*time.Minute))
	book.bump("third", now.Add(4*time.Minute))
	if book.outboundID("base", now.Add(5*time.Minute)) != "" {
		t.Fatal("oldest rotation was not evicted at the cap")
	}
	if book.outboundID("third", now.Add(5*time.Minute)) == "" {
		t.Fatal("newest rotation was evicted")
	}
	if book.outboundID("other", now.Add(2*time.Hour)) != "" {
		t.Fatal("idle rotation did not expire")
	}
}

func TestRewriteBuildSessionFieldsReplacesOnlyPresentSessionIDs(t *testing.T) {
	body := []byte(`{"prompt_cache_key":"client-key","session_id":"sess-1","conversationId":"conv-1","metadata":{"session_id":"meta-sess","tenant":"keep"},"input":"hello","sessionId":""}`)
	rewritten := rewriteBuildSessionFields(body, "rotated-id")
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["prompt_cache_key"] != "rotated-id" || payload["session_id"] != "rotated-id" || payload["conversationId"] != "rotated-id" {
		t.Fatalf("top-level session ids = %#v", payload)
	}
	if payload["input"] != "hello" || payload["sessionId"] != "" {
		t.Fatalf("non-session or empty fields changed: %#v", payload)
	}
	metadata := payload["metadata"].(map[string]any)
	if metadata["session_id"] != "rotated-id" || metadata["tenant"] != "keep" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if _, exists := payload["conversation_id"]; exists {
		t.Fatal("rewrite invented a session field")
	}
	if string(rewriteBuildSessionFields([]byte(`{"input":"hello"}`), "rotated-id")) != `{"input":"hello"}` {
		t.Fatal("body without session fields must stay untouched")
	}
}

func TestMissingReasoningPenaltyRotatesNextBuildSessionOnly(t *testing.T) {
	selector, hot, _ := newFreeBuildUsagePenaltySelector(t)
	service := &Service{selector: selector}
	service.UpdateBuildMissingReasoningPenalty(true, []string{"grok-4.6"}, nil)
	accountID := hot.ID
	record := audit.Record{StatusCode: http.StatusOK, OutputTokens: 21, ReasoningTokens: 0, ModelPublicID: "grok-4.6", AccountID: &accountID}
	body := []byte(`{"prompt_cache_key":"client-key","session_id":"sess-1","metadata":{"conversation_id":"conv-1","tenant":"keep"},"input":"hello"}`)
	credential := account.Credential{ID: hot.ID, Provider: account.ProviderBuild}
	if !service.maybePenalizeBuildMissingReasoning(record, credential, "grok-4.6", body, "base-session") {
		t.Fatal("expected penalty")
	}
	if _, _, ok := service.outboundRotatedBuildSession("base-session", body, time.Now().UTC()); ok {
		t.Fatal("rotation switch defaults off")
	}

	service.UpdateBuildMissingReasoningSessionRotate(true)
	service.UpdateBuildMissingReasoningPenalty(true, []string{"grok-4.6"}, nil)
	if !service.buildMissingReasoningSessionRotateEnabled() {
		t.Fatal("updating the penalty policy cleared session rotation")
	}
	if !service.maybePenalizeBuildMissingReasoning(record, credential, "grok-4.6", body, "base-session") {
		t.Fatal("expected penalty with rotation enabled")
	}
	rotated, rewritten, ok := service.outboundRotatedBuildSession("base-session", body, time.Now().UTC())
	if !ok || rotated == "" || rotated == "base-session" {
		t.Fatalf("rotation = %q ok=%v", rotated, ok)
	}
	again, rewrittenAgain, ok := service.outboundRotatedBuildSession("base-session", body, time.Now().UTC())
	if !ok || again != rotated || string(rewrittenAgain) != string(rewritten) {
		t.Fatal("the same penalized session changed ids before another penalty")
	}
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatal(err)
	}
	metadata := payload["metadata"].(map[string]any)
	if payload["prompt_cache_key"] != rotated || payload["session_id"] != rotated || metadata["conversation_id"] != rotated || metadata["tenant"] != "keep" {
		t.Fatalf("rewritten body = %#v", payload)
	}

	if !service.maybePenalizeBuildMissingReasoning(record, credential, "grok-4.6", body, "base-session") {
		t.Fatal("expected a second penalty")
	}
	next, _, ok := service.outboundRotatedBuildSession("base-session", body, time.Now().UTC())
	if !ok || next == rotated {
		t.Fatalf("second penalty did not rotate, next=%q previous=%q", next, rotated)
	}
	if !service.maybePenalizeBuildMissingReasoning(record, account.Credential{ID: hot.ID, Provider: account.ProviderConsole}, "grok-4.6", body, "console-session") {
		t.Fatal("expected a console penalty")
	}
	consoleBody, ok := service.outboundRotatedConsoleSession("console-session", body, time.Now().UTC())
	if !ok {
		t.Fatal("console penalty should rotate surviving session fields")
	}
	var consolePayload map[string]any
	if err := json.Unmarshal(consoleBody, &consolePayload); err != nil {
		t.Fatal(err)
	}
	consoleMetadata := consolePayload["metadata"].(map[string]any)
	if consolePayload["session_id"] == "sess-1" || consolePayload["session_id"] == "" {
		t.Fatalf("console session_id was not rotated: %#v", consolePayload)
	}
	if consolePayload["prompt_cache_key"] != "client-key" || consoleMetadata["conversation_id"] != "conv-1" || consoleMetadata["tenant"] != "keep" {
		t.Fatalf("console rewrite touched fields that do not survive upstream: %#v", consolePayload)
	}
	service.UpdateBuildMissingReasoningSessionRotate(false)
	if _, _, ok := service.outboundRotatedBuildSession("base-session", body, time.Now().UTC()); ok {
		t.Fatal("disabling the switch still rotated the session")
	}
}
