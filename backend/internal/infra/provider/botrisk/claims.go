// Package botrisk decodes non-sensitive bot-risk claim values from JWT payloads.
// Claim keys are matched case-insensitively; only JSON numbers 1 and 2 count.
package botrisk

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// SourceFromClaims returns the bot-risk source from JWT claims.
// Accepts bot_flag_source, botflagsource, or bfs (case-insensitive keys).
// Only JSON numbers 1 and 2 count (string "1"/"2" do not).
// Preference order: bot_flag_source, botflagsource, bfs.
func SourceFromClaims(claims map[string]any) int {
	if claims == nil {
		return 0
	}
	for _, key := range []string{"bot_flag_source", "botflagsource", "bfs"} {
		if source := sourceClaim(claims, key); source != 0 {
			return source
		}
	}
	return 0
}

// DecodeJWTClaims decodes the middle segment of a compact JWT without verifying
// the signature. Malformed tokens return nil.
func DecodeJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

// SourceFromToken decrypts nothing — it only decodes claims from a raw JWT string.
func SourceFromToken(token string) (source int, inspected bool) {
	claims := DecodeJWTClaims(token)
	if claims == nil {
		return 0, false
	}
	return SourceFromClaims(claims), true
}

func sourceClaim(claims map[string]any, key string) int {
	value, ok := claimValueCaseInsensitive(claims, key)
	if !ok {
		return 0
	}
	number, ok := value.(float64)
	if !ok {
		return 0
	}
	switch number {
	case 1, 2:
		return int(number)
	default:
		return 0
	}
}

func claimValueCaseInsensitive(claims map[string]any, key string) (any, bool) {
	if value, ok := claims[key]; ok {
		return value, true
	}
	target := strings.ToLower(key)
	for candidate, value := range claims {
		if strings.ToLower(candidate) == target {
			return value, true
		}
	}
	return nil, false
}
