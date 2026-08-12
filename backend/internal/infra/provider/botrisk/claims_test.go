package botrisk

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestSourceFromClaimsAliasesAndCase(t *testing.T) {
	if SourceFromClaims(nil) != 0 {
		t.Fatal("nil claims")
	}
	if SourceFromClaims(map[string]any{"bot_flag_source": float64(1)}) != 1 {
		t.Fatal("bot_flag_source=1")
	}
	if SourceFromClaims(map[string]any{"botflagsource": float64(2)}) != 2 {
		t.Fatal("botflagsource=2")
	}
	if SourceFromClaims(map[string]any{"BOTFLAGSOURCE": float64(1)}) != 1 {
		t.Fatal("BOTFLAGSOURCE=1")
	}
	if SourceFromClaims(map[string]any{"bfs": float64(2)}) != 2 {
		t.Fatal("bfs=2")
	}
	if SourceFromClaims(map[string]any{"bot_flag_source": float64(1), "botflagsource": float64(2), "bfs": float64(2)}) != 1 {
		t.Fatal("preference order")
	}
	if SourceFromClaims(map[string]any{"botflagsource": float64(1), "bfs": float64(2)}) != 1 {
		t.Fatal("botflagsource over bfs")
	}
	if SourceFromClaims(map[string]any{"bot_flag_source": "1", "bfs": "2"}) != 0 {
		t.Fatal("strings must not flag")
	}
}

func TestSourceFromToken(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"botflagsource": 2})
	if err != nil {
		t.Fatal(err)
	}
	token := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	source, inspected := SourceFromToken(token)
	if !inspected || source != 2 {
		t.Fatalf("source/inspected = %d/%t", source, inspected)
	}
	if source, inspected := SourceFromToken("not-a-jwt"); inspected || source != 0 {
		t.Fatalf("malformed = %d/%t", source, inspected)
	}
}
