package gateway

import (
	"context"
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
