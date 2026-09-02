package gateway

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	accountapp "github.com/chenyme/grok2api/backend/internal/application/account"
	clientkeyapp "github.com/chenyme/grok2api/backend/internal/application/clientkey"
	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/domain/clientkey"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/runtime/memory"
)

const emptyCapacitySSE = "" +
	": keepalive\n\n" +
	": keepalive\n\n" +
	"event: error\n" +
	`data: {"sequence_number":0,"type":"error","code":null,"message":"The model is currently at capacity due to high demand. Please try again in a few minutes, or use a higher service tier for priority processing: https://docs.x.ai/developers/advanced-api-usage/priority-processing","param":null}` +
	"\n\n"

const immediateCapacitySSE = "" +
	"event: error\n" +
	`data: {"sequence_number":0,"type":"error","code":null,"message":"The model is currently at capacity due to high demand. Please try again in a few minutes.","param":null}` +
	"\n\n"

const anthropicCapacitySSE = "" +
	"event: error\n" +
	`data: {"type":"error","error":{"message":"The model is currently at capacity due to high demand.","type":"api_error"}}` +
	"\n\n"

const tokenGenerationErrorSSE = "" +
	"event: error\n" +
	`data: {"sequence_number":71,"type":"error","code":null,"message":"Internal error during token generation","param":null}` +
	"\n\n"

const outputThenCapacitySSE = "" +
	`data: {"type":"response.output_text.delta","delta":"hello"}` + "\n\n" +
	"event: error\n" +
	`data: {"type":"error","message":"The model is currently at capacity due to high demand."}` +
	"\n\n"

func TestPeekEmptyCapacityStreamRetriesKeepaliveThenCapacity(t *testing.T) {
	prefix, rest, err := peekEmptyCapacityStream(io.NopCloser(strings.NewReader(emptyCapacitySSE)))
	if rest != nil {
		_ = rest.Close()
	}
	if !errors.Is(err, errUpstreamModelAtCapacity) {
		t.Fatalf("err = %v, want errUpstreamModelAtCapacity (prefix=%q)", err, prefix)
	}
}

func TestPeekEmptyCapacityStreamRetriesImmediateCapacity(t *testing.T) {
	_, rest, err := peekEmptyCapacityStream(io.NopCloser(strings.NewReader(immediateCapacitySSE)))
	if rest != nil {
		_ = rest.Close()
	}
	if !errors.Is(err, errUpstreamModelAtCapacity) {
		t.Fatalf("err = %v, want errUpstreamModelAtCapacity", err)
	}
}

func TestPeekEmptyCapacityStreamRetriesAnthropicEnvelope(t *testing.T) {
	_, rest, err := peekEmptyCapacityStream(io.NopCloser(strings.NewReader(anthropicCapacitySSE)))
	if rest != nil {
		_ = rest.Close()
	}
	if !errors.Is(err, errUpstreamModelAtCapacity) {
		t.Fatalf("err = %v, want errUpstreamModelAtCapacity", err)
	}
}

func TestPeekEmptyCapacityStreamDoesNotRetryOtherErrors(t *testing.T) {
	prefix, rest, err := peekEmptyCapacityStream(io.NopCloser(strings.NewReader(tokenGenerationErrorSSE)))
	if rest != nil {
		defer rest.Close()
	}
	if err != nil {
		t.Fatalf("other SSE errors must hand off, err = %v", err)
	}
	leftover, _ := io.ReadAll(rest)
	combined := string(prefix) + string(leftover)
	if !strings.Contains(combined, "Internal error during token generation") {
		t.Fatalf("handed-off stream missing original error: %q", combined)
	}
}

func TestPeekEmptyCapacityStreamDoesNotRetryAfterOutput(t *testing.T) {
	prefix, rest, err := peekEmptyCapacityStream(io.NopCloser(strings.NewReader(outputThenCapacitySSE)))
	if rest != nil {
		defer rest.Close()
	}
	if err != nil {
		t.Fatalf("content then capacity must hand off, err = %v", err)
	}
	leftover, _ := io.ReadAll(rest)
	combined := string(prefix) + string(leftover)
	if !strings.Contains(combined, `"delta":"hello"`) {
		t.Fatalf("handed-off stream missing output delta: %q", combined)
	}
}

func TestGatewayFailsOverEmptyCapacityStreamWithoutPenalty(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "gateway-capacity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accountRepo := relational.NewAccountRepository(database)
	modelRepo := relational.NewModelRepository(database)
	auditRepo := relational.NewAuditRepository(database)
	responseRepo := relational.NewResponseRepository(database)
	keyRepo := relational.NewClientKeyRepository(database)
	first, _, err := accountRepo.UpsertByIdentity(ctx, account.Credential{Provider: account.ProviderBuild, Name: "first", SourceKey: "first", EncryptedAccessToken: "one", ExpiresAt: time.Now().Add(time.Hour), Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 200, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := accountRepo.UpsertByIdentity(ctx, account.Credential{Provider: account.ProviderBuild, Name: "second", SourceKey: "second", EncryptedAccessToken: "two", ExpiresAt: time.Now().Add(time.Hour), Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 100, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := modelRepo.UpsertDiscovered(ctx, account.ProviderBuild, []string{"grok-test"}); err != nil {
		t.Fatal(err)
	}
	for _, accountID := range []uint64{first.ID, second.ID} {
		if err := modelRepo.ReplaceAccountCapabilities(ctx, accountID, []string{"grok-test"}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	clientKey, err := keyRepo.Create(ctx, clientkey.Key{Name: "test-key", Prefix: "test-prefix", SecretHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", EncryptedSecret: "encrypted-key", Enabled: true, RPMLimit: 120, MaxConcurrent: 8})
	if err != nil {
		t.Fatal(err)
	}

	successSSE := `data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n"
	adapter := &failoverAdapter{
		streamBodies: map[uint64]string{
			first.ID:  emptyCapacitySSE,
			second.ID: successSSE,
		},
	}
	registry := provider.NewRegistry(adapter)
	cipher := testCipher(t)
	sticky := memory.NewStickyStore()
	concurrency := memory.NewConcurrencyLimiter()
	accountService := accountapp.NewService(accountRepo, auditRepo, memory.NewDeviceSessionStore(), sticky, registry, cipher, nil)
	clientService := clientkeyapp.NewService(nil, nil, nil, 60, 4, nil)
	selector := NewSelector(accountRepo, concurrency, sticky, registry, time.Hour, time.Second, time.Minute)
	service := NewService(modelRepo, auditRepo, accountService, clientService, registry, selector, responseRepo, 3)
	result, err := service.CreateResponse(ctx, Input{
		RequestID: "req-capacity", ClientKey: clientKey, PublicModel: "grok-test",
		Body: []byte(`{"model":"grok-test"}`), Streaming: true, PromptCacheSeed: "capacity-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatal(err)
	}
	result.Finalize(Usage{Reported: true, OutputTokens: 1, TotalTokens: 1}, "resp-capacity", "")
	_ = result.Body.Close()
	if strings.Contains(string(body), "currently at capacity") {
		t.Fatalf("client saw capacity error instead of failover body: %q", body)
	}
	if !strings.Contains(string(body), `"delta":"ok"`) {
		t.Fatalf("body = %q", body)
	}
	if len(adapter.attempts) != 2 || adapter.attempts[0] != first.ID || adapter.attempts[1] != second.ID {
		t.Fatalf("attempts = %#v", adapter.attempts)
	}
	identity := resolveBuildSessionIdentity(clientKey.ID, account.ProviderBuild, "grok-test", "", "capacity-session", nil)
	if boundID, ok, err := sticky.Get(ctx, stickySessionKey(identity.affinityKey), time.Now().UTC()); err != nil || !ok || boundID != second.ID {
		t.Fatalf("failover sticky binding = %d, %v, err = %v; want account %d", boundID, ok, err, second.ID)
	}
	storedFirst, err := accountRepo.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedFirst.FailureCount != 0 {
		t.Fatalf("capacity failover must not penalize FailureCount, got %d", storedFirst.FailureCount)
	}
	if storedFirst.CooldownUntil != nil && !storedFirst.CooldownUntil.IsZero() && storedFirst.CooldownUntil.After(time.Now().UTC()) {
		t.Fatalf("capacity failover must not set cooldown, got %v", storedFirst.CooldownUntil)
	}
}
