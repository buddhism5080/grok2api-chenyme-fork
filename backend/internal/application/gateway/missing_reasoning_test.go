package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/domain/audit"
	"github.com/chenyme/grok2api/backend/internal/infra/runtime/memory"
)

func TestMissingReasoningPenaltyApplies(t *testing.T) {
	models := map[string]struct{}{"grok-4.6": {}}
	tests := []struct {
		name      string
		provider  account.Provider
		status    int
		output    int64
		reasoning int64
		model     string
		enabled   bool
		want      bool
	}{
		{name: "build 200 output 21 no reasoning grok-4.6", provider: account.ProviderBuild, status: 200, output: 21, model: "grok-4.6", enabled: true, want: true},
		{name: "case and space in model", provider: account.ProviderBuild, status: 200, output: 21, model: " Grok-4.6 ", enabled: true, want: true},
		{name: "switch off", provider: account.ProviderBuild, status: 200, output: 21, model: "grok-4.6", enabled: false},
		{name: "output 20 is not greater", provider: account.ProviderBuild, status: 200, output: 20, model: "grok-4.6", enabled: true},
		{name: "has reasoning", provider: account.ProviderBuild, status: 200, output: 80, reasoning: 1, model: "grok-4.6", enabled: true},
		{name: "not 200", provider: account.ProviderBuild, status: 499, output: 80, model: "grok-4.6", enabled: true},
		{name: "web provider", provider: account.ProviderWeb, status: 200, output: 80, model: "grok-4.6", enabled: true},
		{name: "other model", provider: account.ProviderBuild, status: 200, output: 80, model: "grok-4.20", enabled: true},
		{name: "empty model list", provider: account.ProviderBuild, status: 200, output: 80, model: "grok-4.6", enabled: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			watched := models
			if test.name == "empty model list" {
				watched = nil
			}
			got := missingReasoningPenaltyApplies(test.provider, test.status, test.output, test.reasoning, test.model, test.enabled, watched)
			if got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestAccountPenaltyBookLatchesOnceAndExpires(t *testing.T) {
	book := newAccountPenaltyBook(time.Hour)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	if !book.Latch(9, now) {
		t.Fatal("first latch should apply")
	}
	if !book.Penalized(9, now) {
		t.Fatal("should be penalized after latch")
	}
	if book.Latch(9, now.Add(time.Minute)) {
		t.Fatal("second latch while active should be a no-op")
	}
	if book.Penalized(9, now.Add(time.Hour)) {
		t.Fatal("penalty should expire at TTL")
	}
	if !book.Latch(9, now.Add(time.Hour)) {
		t.Fatal("expired penalty should latch again")
	}
}

func TestSelectorMissingReasoningPenaltyClearsStickyAndDeprioritizes(t *testing.T) {
	ctx := context.Background()
	selector, hot, cold := newFreeBuildUsagePenaltySelector(t)
	sticky := memory.NewStickyStore()
	selector.sticky = sticky
	now := time.Now().UTC()
	if _, err := sticky.Bind(ctx, "sess", hot.ID, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	selector.RecordMissingReasoningPenalty(hot.ID, now)

	id, ok, err := sticky.Get(ctx, "sess", now)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("sticky still bound to %d", id)
	}

	lease, err := selector.Acquire(ctx, account.ProviderBuild, 0, "grok-test", "", "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.Credential.ID != cold.ID {
		t.Fatalf("selected %d, want unpenalized %d", lease.Credential.ID, cold.ID)
	}
}

func TestMaybePenalizeBuildMissingReasoning(t *testing.T) {
	selector, hot, _ := newFreeBuildUsagePenaltySelector(t)
	service := &Service{selector: selector}
	service.UpdateBuildMissingReasoningPenalty(true, []string{" grok-4.6 ", "Grok-4.6"}, nil)
	accountID := hot.ID
	record := audit.Record{
		StatusCode: http.StatusOK, OutputTokens: 21, ReasoningTokens: 0,
		ModelPublicID: "grok-4.6", AccountID: &accountID,
	}
	service.maybePenalizeBuildMissingReasoning(record, account.Credential{ID: hot.ID, Provider: account.ProviderBuild}, "grok-4.6", nil)
	if !selector.schedulingPenalized(hot.ID, time.Now().UTC()) {
		t.Fatal("matching 200/no-reasoning request should penalize scheduling")
	}

	other := &Service{selector: selector}
	other.UpdateBuildMissingReasoningPenalty(true, []string{"grok-4.6"}, nil)
	coldID := uint64(99)
	skip := audit.Record{StatusCode: http.StatusOK, OutputTokens: 21, ReasoningTokens: 4, ModelPublicID: "grok-4.6", AccountID: &coldID}
	other.maybePenalizeBuildMissingReasoning(skip, account.Credential{ID: 99, Provider: account.ProviderBuild}, "grok-4.6", nil)
	if selector.schedulingPenalized(99, time.Now().UTC()) {
		t.Fatal("reasoning tokens > 0 must not penalize")
	}
}

func TestLastConversationTurnIsUserPrompt(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "empty", body: "", want: false},
		{name: "responses string input", body: `{"input":"hello"}`, want: true},
		{name: "responses user message", body: `{"input":[{"role":"user","content":"hello"}]}`, want: true},
		{name: "responses typed user message", body: `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`, want: true},
		{name: "responses function_call_output last", body: `{"input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"c1","output":"done"}]}`, want: false},
		{name: "responses tool_result last", body: `{"input":[{"type":"tool_result","tool_use_id":"t1","content":"done"}]}`, want: false},
		{name: "chat user last", body: `{"messages":[{"role":"user","content":"hello"}]}`, want: true},
		{name: "chat tool last", body: `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[]},{"role":"tool","tool_call_id":"c1","content":"done"}]}`, want: false},
		{name: "anthropic tool_result-only user last", body: `{"messages":[{"role":"user","content":"hi"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`, want: false},
		{name: "anthropic user text plus tool_result", body: `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"},{"type":"text","text":"continue"}]}]}`, want: true},
		{name: "assistant last", body: `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]}`, want: false},
		{name: "prefer input over messages", body: `{"input":[{"type":"function_call_output","call_id":"c1","output":"x"}],"messages":[{"role":"user","content":"hi"}]}`, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := lastConversationTurnIsUserPrompt([]byte(test.body))
			if got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestMaybePenalizeSkipsToolResultTurnsForUserTurnModels(t *testing.T) {
	selector, hot, _ := newFreeBuildUsagePenaltySelector(t)
	service := &Service{selector: selector}
	service.UpdateBuildMissingReasoningPenalty(true, []string{"grok-4.7", "grok-4.6"}, []string{"grok-4.7"})
	accountID := hot.ID
	record := audit.Record{
		StatusCode: http.StatusOK, OutputTokens: 21, ReasoningTokens: 0,
		ModelPublicID: "grok-4.7", AccountID: &accountID,
	}
	toolBody := []byte(`{"input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`)
	service.maybePenalizeBuildMissingReasoning(record, account.Credential{ID: hot.ID, Provider: account.ProviderBuild}, "grok-4.7", toolBody)
	if selector.schedulingPenalized(hot.ID, time.Now().UTC()) {
		t.Fatal("tool-result last turn must not penalize user-turn models")
	}

	userBody := []byte(`{"input":[{"role":"user","content":"write code"}]}`)
	service.maybePenalizeBuildMissingReasoning(record, account.Credential{ID: hot.ID, Provider: account.ProviderBuild}, "grok-4.7", userBody)
	if !selector.schedulingPenalized(hot.ID, time.Now().UTC()) {
		t.Fatal("user-prompt last turn should still penalize")
	}
}

func TestMaybePenalizeToolResultStillHitsUngatedModels(t *testing.T) {
	selector, hot, _ := newFreeBuildUsagePenaltySelector(t)
	service := &Service{selector: selector}
	service.UpdateBuildMissingReasoningPenalty(true, []string{"grok-4.6"}, []string{"grok-4.7"})
	accountID := hot.ID
	record := audit.Record{
		StatusCode: http.StatusOK, OutputTokens: 21, ReasoningTokens: 0,
		ModelPublicID: "grok-4.6", AccountID: &accountID,
	}
	toolBody := []byte(`{"messages":[{"role":"tool","tool_call_id":"c1","content":"done"}]}`)
	service.maybePenalizeBuildMissingReasoning(record, account.Credential{ID: hot.ID, Provider: account.ProviderBuild}, "grok-4.6", toolBody)
	if !selector.schedulingPenalized(hot.ID, time.Now().UTC()) {
		t.Fatal("models outside the user-turn list keep the original penalty")
	}
}
