package gateway

import (
	"testing"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/domain/audit"
)

func TestAuditOutputTokensPerSecondMatchesPanel(t *testing.T) {
	first := int64(250)
	record := audit.Record{
		Streaming:    true,
		StatusCode:   200,
		FirstTokenMS: &first,
		DurationMS:   1250,
		OutputTokens: 80,
	}
	got, ok := auditOutputTokensPerSecond(record)
	if !ok || got != 80 {
		t.Fatalf("got %v ok=%v, want 80 true", got, ok)
	}
}

func TestAuditOutputTokensPerSecondRequiresStreamSuccess(t *testing.T) {
	first := int64(100)
	cases := []audit.Record{
		{Streaming: false, StatusCode: 200, FirstTokenMS: &first, DurationMS: 1100, OutputTokens: 100},
		{Streaming: true, StatusCode: 500, FirstTokenMS: &first, DurationMS: 1100, OutputTokens: 100},
		{Streaming: true, StatusCode: 200, FirstTokenMS: nil, DurationMS: 1100, OutputTokens: 100},
		{Streaming: true, StatusCode: 200, FirstTokenMS: &first, DurationMS: 100, OutputTokens: 100},
		{Streaming: true, StatusCode: 200, FirstTokenMS: &first, DurationMS: 1100, OutputTokens: 0},
		{Streaming: true, StatusCode: 200, ErrorCode: "stream_closed", FirstTokenMS: &first, DurationMS: 1100, OutputTokens: 100},
	}
	for i, record := range cases {
		if _, ok := auditOutputTokensPerSecond(record); ok {
			t.Fatalf("case %d unexpectedly measured", i)
		}
	}
}

func TestHighTokenSpeedForAutoDisableSkipsShortOutput(t *testing.T) {
	first := int64(200)
	record := audit.Record{
		Streaming: true, StatusCode: 200, FirstTokenMS: &first,
		DurationMS: 5000, OutputTokens: 999,
	}
	if _, _, ok := highTokenSpeedForAutoDisable(record, 2000); ok {
		t.Fatal("output < 1000 must be skipped")
	}
}

func TestHighTokenSpeedForAutoDisableUsesFixedOverheadNotAuditFirstToken(t *testing.T) {
	// Network delay: first byte arrives at 10s, then buffered tokens dump in 0.5s.
	// Audit panel formula: 2000 * 1000 / 500 = 4000 tok/s (false high).
	// Auto-disable with fixed overhead 2000ms → effective = 10500-2000 = 8500 → ~235 tok/s.
	first := int64(10000)
	record := audit.Record{
		Streaming: true, StatusCode: 200, FirstTokenMS: &first,
		DurationMS: 10500, OutputTokens: 2000,
	}
	speed, effectiveMS, ok := highTokenSpeedForAutoDisable(record, 2000)
	if !ok {
		t.Fatal("expected measurable speed")
	}
	if effectiveMS != 8500 {
		t.Fatalf("effectiveMS = %d, want 8500", effectiveMS)
	}
	want := float64(2000) * 1000 / 8500
	if speed != want {
		t.Fatalf("speed = %v, want %v", speed, want)
	}
	if speed >= 1000 {
		t.Fatalf("buffered dump must not look like high TPS: %v", speed)
	}
}

func TestHighTokenSpeedForAutoDisableKeepsRealFastStreamHigh(t *testing.T) {
	// Real fast stream: finishes 2.2s, 3000 tokens, fixed overhead 2000ms.
	// effective = 2200-2000 = 200 → 15000 tok/s.
	first := int64(200)
	record := audit.Record{
		Streaming: true, StatusCode: 200, FirstTokenMS: &first,
		DurationMS: 2200, OutputTokens: 3000,
	}
	speed, effectiveMS, ok := highTokenSpeedForAutoDisable(record, 2000)
	if !ok || effectiveMS != 200 {
		t.Fatalf("effectiveMS=%d ok=%v", effectiveMS, ok)
	}
	want := float64(3000) * 1000 / 200
	if speed != want {
		t.Fatalf("speed = %v, want %v", speed, want)
	}
	if speed < 1000 {
		t.Fatalf("real high speed should still measure high: %v", speed)
	}
}

func TestHighTokenSpeedForAutoDisableRequiresPositiveEffectiveWindow(t *testing.T) {
	first := int64(100)
	record := audit.Record{
		Streaming: true, StatusCode: 200, FirstTokenMS: &first,
		DurationMS: 1500, OutputTokens: 2000, // effective = 1500-2000 < 0
	}
	if _, _, ok := highTokenSpeedForAutoDisable(record, 2000); ok {
		t.Fatal("duration <= overhead must be skipped")
	}
}

func TestBuildHighTokenSpeedPolicyMatchesConfiguredModels(t *testing.T) {
	service := &Service{}
	service.UpdateBuildHighTokenSpeedAutoDisable(true, 1000, 2000, []string{" grok-4.20 ", "Grok-4.20", "build/ignored"})
	service.buildHighTokenSpeedMu.RLock()
	defer service.buildHighTokenSpeedMu.RUnlock()
	if !service.buildHighTokenSpeedPolicy.enabled || service.buildHighTokenSpeedPolicy.threshold != 1000 || service.buildHighTokenSpeedPolicy.overheadMS != 2000 {
		t.Fatalf("policy = %#v", service.buildHighTokenSpeedPolicy)
	}
	if _, ok := service.buildHighTokenSpeedPolicy.models["grok-4.20"]; !ok {
		t.Fatalf("missing model map: %#v", service.buildHighTokenSpeedPolicy.models)
	}
	if len(service.buildHighTokenSpeedPolicy.models) != 2 {
		t.Fatalf("expected 2 unique models, got %#v", service.buildHighTokenSpeedPolicy.models)
	}
}

func TestMaybeDisableRequiresBuildProviderAndWatchedModel(t *testing.T) {
	service := &Service{}
	service.UpdateBuildHighTokenSpeedAutoDisable(true, 1000, 2000, []string{"grok-4.20"})
	first := int64(100)
	record := audit.Record{
		Streaming: true, StatusCode: 200, FirstTokenMS: &first, DurationMS: 200, OutputTokens: 500, ModelPublicID: "grok-4.20",
	}
	// No accounts service: calling with non-Build must no-op without panic.
	service.maybeDisableBuildAccountForHighTokenSpeed(nil, record, accountdomain.Credential{Provider: accountdomain.ProviderWeb, ID: 1}, "grok-4.20")
	// Below threshold / short output must no-op.
	low := audit.Record{Streaming: true, StatusCode: 200, FirstTokenMS: &first, DurationMS: 2000, OutputTokens: 100, ModelPublicID: "grok-4.20", AccountID: uint64Ptr(1)}
	service.maybeDisableBuildAccountForHighTokenSpeed(nil, low, accountdomain.Credential{Provider: accountdomain.ProviderBuild, ID: 1}, "grok-4.20")
}

func uint64Ptr(value uint64) *uint64 { return &value }
