package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
)

const maxBuildSessionRotations = 8192

// sessionRotationEntry remembers the upstream session id currently substituted
// for one penalized Build conversation. The key is the stable pre-rotation
// identity, so later turns keep finding the same substitution until the next
// penalty. Bodies are never stored.
type sessionRotationEntry struct {
	generation uint32
	outboundID string
	lastAccess time.Time
}

type sessionRotationBook struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]sessionRotationEntry
}

func newSessionRotationBook(ttl time.Duration, maxEntries int) *sessionRotationBook {
	if ttl <= 0 {
		ttl = defaultMissingReasoningPenaltyTTL
	}
	if maxEntries <= 0 {
		maxEntries = maxBuildSessionRotations
	}
	return &sessionRotationBook{ttl: ttl, max: maxEntries, entries: make(map[string]sessionRotationEntry)}
}

func (b *sessionRotationBook) outboundID(key string, now time.Time) string {
	if b == nil || key == "" {
		return ""
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.entries[key]
	if !ok {
		return ""
	}
	if now.Sub(entry.lastAccess) > b.ttl {
		delete(b.entries, key)
		return ""
	}
	entry.lastAccess = now
	b.entries[key] = entry
	return entry.outboundID
}

func (b *sessionRotationBook) bump(key string, now time.Time) string {
	if b == nil || key == "" {
		return ""
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dropExpiredLocked(now)
	entry := b.entries[key]
	if entry.generation == ^uint32(0) {
		entry.generation = 1
	} else {
		entry.generation++
	}
	// generation keeps two penalties in the same nanosecond from hashing to the same id.
	entry.outboundID = digestUUID(fmt.Sprintf("grok2api:build-session-rotate:v1:%s:%d:%d", key, entry.generation, now.UnixNano()))
	entry.lastAccess = now
	b.entries[key] = entry
	b.evictOldestLocked()
	return b.entries[key].outboundID
}

func (b *sessionRotationBook) dropExpiredLocked(now time.Time) {
	for key, entry := range b.entries {
		if now.Sub(entry.lastAccess) > b.ttl {
			delete(b.entries, key)
		}
	}
}

func (b *sessionRotationBook) evictOldestLocked() {
	for len(b.entries) > b.max {
		var oldestKey string
		var oldest time.Time
		first := true
		for key, entry := range b.entries {
			if first || entry.lastAccess.Before(oldest) {
				first = false
				oldest = entry.lastAccess
				oldestKey = key
			}
		}
		if oldestKey == "" {
			return
		}
		delete(b.entries, oldestKey)
	}
}

func (s *Service) sessionRotationBook() *sessionRotationBook {
	if s == nil {
		return nil
	}
	s.sessionRotateOnce.Do(func() {
		if s.sessionRotations == nil {
			s.sessionRotations = newSessionRotationBook(defaultMissingReasoningPenaltyTTL, maxBuildSessionRotations)
		}
	})
	return s.sessionRotations
}

func (s *Service) buildMissingReasoningSessionRotateEnabled() bool {
	if s == nil {
		return false
	}
	s.buildMissingReasoningMu.RLock()
	enabled := s.buildMissingReasoningPolicy.sessionRotate
	s.buildMissingReasoningMu.RUnlock()
	return enabled
}

// UpdateBuildMissingReasoningSessionRotate 热更新「无推理惩罚后轮换 Build 上游会话」。
// 默认关闭。开启后，已触发惩罚的 Build 会话从下一次请求起改用新的上游会话 id。
func (s *Service) UpdateBuildMissingReasoningSessionRotate(enabled bool) {
	if s == nil {
		return
	}
	s.buildMissingReasoningMu.Lock()
	s.buildMissingReasoningPolicy.sessionRotate = enabled
	s.buildMissingReasoningMu.Unlock()
}

// outboundRotatedBuildSession returns the substitute upstream session id for a
// penalized Build conversation. The stored ownership key stays the base id;
// only the id sent upstream changes. ok is false when the switch is off, the
// session has not been penalized, or the entry has expired.
func (s *Service) outboundRotatedBuildSession(base string, body []byte, now time.Time) (string, []byte, bool) {
	base = strings.TrimSpace(base)
	if s == nil || base == "" || !s.buildMissingReasoningSessionRotateEnabled() {
		return "", nil, false
	}
	rotated := s.sessionRotationBook().outboundID(base, now)
	if rotated == "" || rotated == base {
		return "", nil, false
	}
	return rotated, rewriteBuildSessionFields(body, rotated), true
}

// outboundRotatedConsoleSession rewrites only the session fields Console
// actually forwards. prompt_cache_key, metadata, and the Grok session headers
// are not part of the Console protocol and are left alone.
func (s *Service) outboundRotatedConsoleSession(base string, body []byte, now time.Time) ([]byte, bool) {
	base = strings.TrimSpace(base)
	if s == nil || base == "" || !s.buildMissingReasoningSessionRotateEnabled() {
		return nil, false
	}
	rotated := s.sessionRotationBook().outboundID(base, now)
	if rotated == "" || rotated == base {
		return nil, false
	}
	rewritten := rewriteSessionFields(body, rotated, consoleSessionFields, nil)
	if bytes.Equal(rewritten, body) {
		return nil, false
	}
	return rewritten, true
}

func (s *Service) noteBuildSessionPenalty(provider accountdomain.Provider, sessionKey string, now time.Time) {
	sessionKey = strings.TrimSpace(sessionKey)
	if s == nil || sessionKey == "" || !s.buildMissingReasoningSessionRotateEnabled() {
		return
	}
	if provider != accountdomain.ProviderBuild && provider != accountdomain.ProviderConsole {
		return
	}
	if s.sessionRotationBook().bump(sessionKey, now) == "" {
		return
	}
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("build_missing_reasoning_session_rotated")
}

var buildSessionTopLevelFields = []string{"prompt_cache_key", "session_id", "sessionId", "conversation_id", "conversationId"}
var buildSessionMetadataFields = []string{"session_id", "sessionId", "conversation_id", "conversationId"}
var consoleSessionFields = []string{"session_id", "sessionId", "conversation_id", "conversationId"}

// rewriteBuildSessionFields replaces client session identifiers that would
// otherwise be forwarded upstream. Fields that are absent, empty, or not
// strings are left untouched, and no new session fields are invented.
func rewriteBuildSessionFields(body []byte, sessionID string) []byte {
	return rewriteSessionFields(body, sessionID, buildSessionTopLevelFields, buildSessionMetadataFields)
}

func rewriteSessionFields(body []byte, sessionID string, topLevel, metadataFields []string) []byte {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(bytes.TrimSpace(body)) == 0 {
		return body
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return body
	}
	changed := false
	for _, key := range topLevel {
		if rewriteJSONStringField(payload, key, sessionID) {
			changed = true
		}
	}
	if len(metadataFields) > 0 {
		if raw, ok := payload["metadata"]; ok && !jsonRawEmpty(raw) {
			var metadata map[string]json.RawMessage
			if json.Unmarshal(raw, &metadata) == nil && metadata != nil {
				metadataChanged := false
				for _, key := range metadataFields {
					if rewriteJSONStringField(metadata, key, sessionID) {
						metadataChanged = true
					}
				}
				if metadataChanged {
					encoded, err := json.Marshal(metadata)
					if err == nil {
						payload["metadata"] = encoded
						changed = true
					}
				}
			}
		}
	}
	if !changed {
		return body
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return encoded
}

func rewriteJSONStringField(payload map[string]json.RawMessage, key, value string) bool {
	raw, ok := payload[key]
	if !ok || jsonRawEmpty(raw) {
		return false
	}
	var current string
	if json.Unmarshal(raw, &current) != nil || strings.TrimSpace(current) == "" || current == value {
		return false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	payload[key] = encoded
	return true
}
