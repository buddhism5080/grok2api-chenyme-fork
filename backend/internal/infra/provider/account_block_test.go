package provider

import "testing"

func TestIsDefinitiveAccountBlockBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "top-level message", body: `{"code":7,"message":"User is blocked [WKE=unauthorized:blocked-user]"}`, want: true},
		{name: "nested error", body: `{"error":{"code":7,"message":"User is blocked [WKE=unauthorized:blocked-user]"}}`, want: true},
		{name: "console error", body: `{"code":"unauthorized:blocked-user","error":"User is blocked"}`, want: true},
		{name: "plain text", body: `User is blocked`, want: true},
		{name: "generic rejection", body: `{"message":"temporary rejection"}`, want: false},
		{name: "unrelated details", body: `{"message":"temporary rejection","details":["blocked-user is an internal enum"]}`, want: false},
		{name: "numeric code only", body: `{"code":7,"message":"anti-bot rejection"}`, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsDefinitiveAccountBlockBody([]byte(test.body)); got != test.want {
				t.Fatalf("IsDefinitiveAccountBlockBody() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestIsCannotHelpRefusalBody(t *testing.T) {
	t.Parallel()
	body := []byte(`{"code":"permission-denied","error":"I'm sorry, I can't help with that request."}`)
	if !IsCannotHelpRefusalBody(body) {
		t.Fatal("model refusal must be recognized")
	}
	if IsCannotHelpRefusalBody([]byte(`{"code":"permission-denied","error":"Access to the chat endpoint is denied"}`)) {
		t.Fatal("account permission denial is not this refusal")
	}
	if IsCannotHelpRefusalBody([]byte(`<!DOCTYPE html><title>Just a moment...</title>`)) {
		t.Fatal("cloudflare challenge is not this refusal")
	}
}

func TestIsDPoPProofRequiredBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		body string
		want bool
	}{
		{body: `{"code":"unauthorized:dpop-required"}`, want: true},
		{body: `{"error":{"code":"unauthorized","message":"DPoP proof required"}}`, want: true},
		{body: `unauthorized:dpop-required`, want: true},
		{body: `{"code":"forbidden","message":"account denied"}`, want: false},
	}
	for _, test := range tests {
		if got := IsDPoPProofRequiredBody([]byte(test.body)); got != test.want {
			t.Fatalf("IsDPoPProofRequiredBody(%q) = %v, want %v", test.body, got, test.want)
		}
	}
}
