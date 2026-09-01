package gateway

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
)

const defaultBuildUsagePenaltyTTL = 24 * time.Hour

type buildUsagePenaltyEntry struct {
	Tokens       int64
	PenaltyUntil time.Time
}

type buildUsagePenaltyBook struct {
	mu        sync.Mutex
	threshold int64
	ttl       time.Duration
	entries   map[uint64]buildUsagePenaltyEntry
}

type buildUsagePenaltyStore interface {
	UpsertBuildUsagePenalty(ctx context.Context, value account.BuildUsagePenalty) error
	ListBuildUsagePenalties(ctx context.Context) ([]account.BuildUsagePenalty, error)
}

func newBuildUsagePenaltyBook() *buildUsagePenaltyBook {
	return &buildUsagePenaltyBook{ttl: defaultBuildUsagePenaltyTTL, entries: make(map[uint64]buildUsagePenaltyEntry)}
}

func buildUsagePenaltyTokens(input, output int64) int64 {
	if input < 0 {
		input = 0
	}
	if output < 0 {
		output = 0
	}
	if output > 0 && input > math.MaxInt64-output {
		return math.MaxInt64
	}
	return input + output
}

func (b *buildUsagePenaltyBook) UpdateThreshold(threshold int64) {
	if threshold < 0 {
		threshold = 0
	}
	b.mu.Lock()
	b.threshold = threshold
	b.mu.Unlock()
}

func (b *buildUsagePenaltyBook) Penalized(accountID uint64, now time.Time) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.penalizedLocked(accountID, now)
}

func (b *buildUsagePenaltyBook) penalizedLocked(accountID uint64, now time.Time) bool {
	if b.threshold <= 0 {
		return false
	}
	entry, ok := b.entries[accountID]
	if !ok || entry.PenaltyUntil.IsZero() {
		return false
	}
	if !now.Before(entry.PenaltyUntil) {
		delete(b.entries, accountID)
		return false
	}
	return true
}

func (b *buildUsagePenaltyBook) penaltyUntil(accountID uint64) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.entries[accountID].PenaltyUntil
}

func (b *buildUsagePenaltyBook) Record(candidate account.RoutingCandidate, input, output int64, now time.Time) (latched bool) {
	tokens := buildUsagePenaltyTokens(input, output)
	if tokens <= 0 || !candidate.IsKnownFreeBuild() {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.threshold <= 0 {
		return false
	}
	accountID := candidate.Credential.ID
	entry := b.entries[accountID]
	if !entry.PenaltyUntil.IsZero() && !now.Before(entry.PenaltyUntil) {
		entry = buildUsagePenaltyEntry{}
	}
	sum := entry.Tokens
	if tokens > 0 && sum > math.MaxInt64-tokens {
		sum = math.MaxInt64
	} else {
		sum += tokens
	}
	entry.Tokens = sum
	if entry.PenaltyUntil.IsZero() && entry.Tokens >= b.threshold {
		entry.PenaltyUntil = now.Add(b.ttl)
		latched = true
	}
	b.entries[accountID] = entry
	return latched
}

func (b *buildUsagePenaltyBook) snapshot(accountID uint64) (buildUsagePenaltyEntry, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.entries[accountID]
	return entry, ok
}

func (b *buildUsagePenaltyBook) Load(values []account.BuildUsagePenalty, now time.Time) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entries == nil {
		b.entries = make(map[uint64]buildUsagePenaltyEntry, len(values))
	}
	for _, value := range values {
		if value.AccountID == 0 {
			continue
		}
		if !value.PenaltyUntil.IsZero() && !now.Before(value.PenaltyUntil) {
			continue
		}
		b.entries[value.AccountID] = buildUsagePenaltyEntry{Tokens: value.Tokens, PenaltyUntil: value.PenaltyUntil}
	}
}
