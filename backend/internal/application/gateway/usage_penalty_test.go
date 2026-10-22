package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/runtime/memory"
)

func TestBuildUsagePenaltyTokensSumsInputAndOutput(t *testing.T) {
	if got := buildUsagePenaltyTokens(10, 5); got != 15 {
		t.Fatalf("tokens = %d, want 15", got)
	}
	if got := buildUsagePenaltyTokens(-1, 8); got != 8 {
		t.Fatalf("negative input = %d, want 8", got)
	}
	if got := buildUsagePenaltyTokens(0, 0); got != 0 {
		t.Fatalf("zero = %d", got)
	}
}

func TestBuildUsagePenaltyBookLatchesOnceForFreeBuild(t *testing.T) {
	book := newBuildUsagePenaltyBook()
	book.UpdateThreshold(100)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	free := account.RoutingCandidate{
		Credential: account.Credential{ID: 7, Provider: account.ProviderBuild, ObservedModel: "grok-4-build-free"},
	}
	super := account.RoutingCandidate{
		Credential: account.Credential{ID: 8, Provider: account.ProviderBuild, BuildSuperEntitled: true, ObservedModel: "grok-4-build-free"},
	}
	unknown := account.RoutingCandidate{
		Credential: account.Credential{ID: 9, Provider: account.ProviderBuild, ObservedModel: "grok-4"},
	}

	book.Record(free, 40, 50, now)
	if book.Penalized(7, now) {
		t.Fatal("should not latch below threshold")
	}
	book.Record(free, 5, 5, now.Add(time.Minute))
	if !book.Penalized(7, now.Add(time.Minute)) {
		t.Fatal("should latch once input+output reaches threshold")
	}
	until := book.penaltyUntil(7)
	book.Record(free, 10_000, 10_000, now.Add(2*time.Hour))
	if !book.penaltyUntil(7).Equal(until) {
		t.Fatalf("latch extended: %s -> %s", until, book.penaltyUntil(7))
	}
	expiredAt := now.Add(time.Minute + 24*time.Hour)
	if book.Penalized(7, expiredAt) {
		t.Fatal("penalty should clear at 24h")
	}
	book.Record(free, 1, 0, expiredAt.Add(time.Second))
	if book.Penalized(7, expiredAt.Add(time.Second)) {
		t.Fatal("fresh window after expiry should not immediately re-latch")
	}

	book.Record(super, 500, 500, now)
	if book.Penalized(8, now) {
		t.Fatal("Super Build accounts must not be penalized")
	}
	book.Record(unknown, 500, 500, now)
	if book.Penalized(9, now) {
		t.Fatal("unconfirmed Build accounts must not be penalized")
	}
}

func TestBuildUsagePenaltyBookThresholdZeroDisables(t *testing.T) {
	book := newBuildUsagePenaltyBook()
	now := time.Now().UTC()
	free := account.RoutingCandidate{
		Credential: account.Credential{ID: 3, Provider: account.ProviderBuild, ObservedModel: "grok-4-build-free"},
	}
	book.Record(free, 100, 100, now)
	if book.Penalized(3, now) {
		t.Fatal("default threshold 0 must disable the guard")
	}
	book.UpdateThreshold(50)
	book.Record(free, 20, 30, now)
	if !book.Penalized(3, now) {
		t.Fatal("positive threshold should latch")
	}
	book.UpdateThreshold(0)
	if book.Penalized(3, now) {
		t.Fatal("turning the threshold off should drop the penalty")
	}
}

func TestSelectorPrefersUnpenalizedFreeBuildOverHigherPriority(t *testing.T) {
	ctx := context.Background()
	selector, hot, cold := newFreeBuildUsagePenaltySelector(t)
	selector.UpdateBuildUsagePenaltyTokenThreshold(20)
	selector.RecordBuildUsage(freeBuildCandidate(hot), 15, 10, time.Now().UTC())

	lease, err := selector.Acquire(ctx, account.ProviderBuild, 0, "grok-test", "", "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.Credential.ID != cold.ID {
		t.Fatalf("selected %d, want unpenalized account %d", lease.Credential.ID, cold.ID)
	}
}

func TestSelectorStillPicksWhenEveryFreeAccountIsPenalized(t *testing.T) {
	ctx := context.Background()
	selector, hot, cold := newFreeBuildUsagePenaltySelector(t)
	selector.UpdateBuildUsagePenaltyTokenThreshold(10)
	now := time.Now().UTC()
	selector.RecordBuildUsage(freeBuildCandidate(hot), 10, 1, now)
	selector.RecordBuildUsage(freeBuildCandidate(cold), 10, 1, now)

	lease, err := selector.Acquire(ctx, account.ProviderBuild, 0, "grok-test", "", "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.Credential.ID != hot.ID && lease.Credential.ID != cold.ID {
		t.Fatalf("selected %d, want a penalized fallback", lease.Credential.ID)
	}
}

func TestSelectorSkipsStickyPenalizedFreeAccount(t *testing.T) {
	ctx := context.Background()
	selector, hot, cold := newFreeBuildUsagePenaltySelector(t)
	selector.UpdateBuildUsagePenaltyTokenThreshold(10)
	now := time.Now().UTC()
	if err := selector.sticky.Set(ctx, stickySessionKey("session-a"), hot.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	selector.RecordBuildUsage(freeBuildCandidate(hot), 20, 0, now)

	lease, err := selector.Acquire(ctx, account.ProviderBuild, 0, "grok-test", "", "session-a", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.Credential.ID != cold.ID {
		t.Fatalf("sticky selected %d, want cold %d", lease.Credential.ID, cold.ID)
	}
}

func TestSelectorPinnedPenalizedFreeAccountStillBinds(t *testing.T) {
	ctx := context.Background()
	selector, hot, _ := newFreeBuildUsagePenaltySelector(t)
	selector.UpdateBuildUsagePenaltyTokenThreshold(10)
	selector.RecordBuildUsage(freeBuildCandidate(hot), 20, 0, time.Now().UTC())

	lease, err := selector.AcquirePinned(ctx, account.ProviderBuild, hot.ID, 0, "grok-test", "", true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.Credential.ID != hot.ID {
		t.Fatalf("pinned selected %d, want hot %d", lease.Credential.ID, hot.ID)
	}
}

func TestSelectorUsagePenaltySurvivesReload(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "usage-penalty-reload.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	hot, _, err := accounts.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderBuild, Name: "hot", SourceKey: "hot", EncryptedAccessToken: "encrypted",
		Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 200, MaxConcurrent: 1,
		ObservedModel: "grok-4-build-free",
	})
	if err != nil {
		t.Fatal(err)
	}
	first := NewSelector(accounts, memory.NewConcurrencyLimiter(), memory.NewStickyStore(), nil, time.Hour, time.Second, time.Minute)
	first.UpdateBuildUsagePenaltyTokenThreshold(10)
	first.RecordBuildUsage(freeBuildCandidate(hot), 12, 0, time.Now().UTC())

	second := NewSelector(accounts, memory.NewConcurrencyLimiter(), memory.NewStickyStore(), nil, time.Hour, time.Second, time.Minute)
	second.UpdateBuildUsagePenaltyTokenThreshold(10)
	if !second.usagePenalty.Penalized(hot.ID, time.Now().UTC()) {
		t.Fatal("reloaded selector lost the 24h usage penalty latch")
	}
}

func TestCandidateScorePrefersUnpenalizedBeforePriority(t *testing.T) {
	values := []account.RoutingCandidate{
		{Credential: account.Credential{ID: 1, Priority: 200}},
		{Credential: account.Credential{ID: 2, Priority: 1}},
	}
	if !candidateScoreBetter(values, candidateScore{index: 1, usagePenalized: false}, candidateScore{index: 0, usagePenalized: true}) {
		t.Fatal("unpenalized lower-priority account should rank better")
	}
}

func newFreeBuildUsagePenaltySelector(t *testing.T) (*Selector, account.Credential, account.Credential) {
	t.Helper()
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "usage-penalty.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	hot, _, err := accounts.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderBuild, Name: "hot", SourceKey: "hot", EncryptedAccessToken: "encrypted",
		Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 200, MaxConcurrent: 1,
		ObservedModel: "grok-4-build-free",
	})
	if err != nil {
		t.Fatal(err)
	}
	cold, _, err := accounts.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderBuild, Name: "cold", SourceKey: "cold", EncryptedAccessToken: "encrypted",
		Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 1, MaxConcurrent: 1,
		ObservedModel: "grok-4-build-free",
	})
	if err != nil {
		t.Fatal(err)
	}
	selector := NewSelector(accounts, memory.NewConcurrencyLimiter(), memory.NewStickyStore(), nil, time.Hour, time.Second, time.Minute)
	return selector, hot, cold
}

func freeBuildCandidate(value account.Credential) account.RoutingCandidate {
	return account.RoutingCandidate{Credential: value}
}
