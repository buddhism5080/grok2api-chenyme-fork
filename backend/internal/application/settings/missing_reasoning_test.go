package settings

import (
	"context"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/infra/config"
)

func TestUpdateAppliesBuildMissingReasoningPenalty(t *testing.T) {
	cfg := testConfig(t)
	repository := &runtimeSettingsRepositoryStub{}
	var applied config.Config
	service := NewService(cfg, time.Time{}, 0, repository, nil, func(next config.Config) { applied = next })
	input := service.Get().Config
	input.Routing.BuildMissingReasoningPenaltyEnabled = true
	input.Routing.BuildMissingReasoningPenaltyEnabledProvided = true
	input.Routing.BuildMissingReasoningPenaltyModelIDs = []string{" grok-4.6 ", "Grok-4.6", "grok-4.20"}
	input.Routing.BuildMissingReasoningPenaltyModelIDsProvided = true

	snapshot, err := service.Update(context.Background(), service.Get().Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Routing.BuildMissingReasoningPenaltyEnabled {
		t.Fatal("missing-reasoning penalty was not applied")
	}
	got := applied.Routing.BuildMissingReasoningPenaltyModelIDs
	if len(got) != 2 || got[0] != "grok-4.6" || got[1] != "grok-4.20" {
		t.Fatalf("applied models = %#v", got)
	}
	if snapshot.Config.Routing.BuildMissingReasoningPenaltyEnabled != true {
		t.Fatal("snapshot lost enabled flag")
	}
}

func TestUpdatePreservesBuildMissingReasoningPenaltyWhenOmitted(t *testing.T) {
	cfg := testConfig(t)
	cfg.Routing.BuildMissingReasoningPenaltyEnabled = true
	cfg.Routing.BuildMissingReasoningPenaltyModelIDs = []string{"grok-4.6"}
	repository := &runtimeSettingsRepositoryStub{}
	var applied config.Config
	service := NewService(cfg, time.Time{}, 0, repository, nil, func(next config.Config) { applied = next })
	input := service.Get().Config
	input.Routing.BuildMissingReasoningPenaltyEnabled = false
	input.Routing.BuildMissingReasoningPenaltyEnabledProvided = false
	input.Routing.BuildMissingReasoningPenaltyModelIDs = nil
	input.Routing.BuildMissingReasoningPenaltyModelIDsProvided = false

	if _, err := service.Update(context.Background(), 0, input); err != nil {
		t.Fatal(err)
	}
	if !applied.Routing.BuildMissingReasoningPenaltyEnabled {
		t.Fatal("omitted missing-reasoning switch overwrote the current value")
	}
	got := applied.Routing.BuildMissingReasoningPenaltyModelIDs
	if len(got) != 1 || got[0] != "grok-4.6" {
		t.Fatalf("omitted missing-reasoning models overwrote the current value: %#v", got)
	}
}

func TestLoadPersistedKeepsMissingReasoningDefaultsForOlderPayload(t *testing.T) {
	cfg := testConfig(t)
	cfg.Routing.BuildMissingReasoningPenaltyEnabled = false
	cfg.Routing.BuildMissingReasoningPenaltyModelIDs = []string{"grok-4.6"}
	value := toDomainConfig(cfg)
	value.Routing.BuildMissingReasoningPenaltyEnabled = nil
	value.Routing.BuildMissingReasoningPenaltyModelIDs = nil
	repository := &runtimeSettingsRepositoryStub{value: value, found: true}

	loaded, _, _, err := LoadPersisted(context.Background(), cfg, repository)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Routing.BuildMissingReasoningPenaltyEnabled {
		t.Fatal("older payload should keep the default-off switch")
	}
	got := loaded.Routing.BuildMissingReasoningPenaltyModelIDs
	if len(got) != 1 || got[0] != "grok-4.6" {
		t.Fatalf("older payload lost default models: %#v", got)
	}
}
