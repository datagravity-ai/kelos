package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// computeHMAC is a test helper that computes the HMAC-SHA256 hex digest for a payload.
func computeHMAC(payload, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestValidateGitHubSignature(t *testing.T) {
	secret := []byte("my-secret-key")
	payload := []byte(`{"action":"opened","number":1}`)
	validSig := "sha256=" + computeHMAC(payload, secret)

	tests := []struct {
		name      string
		signature string
		wantErr   bool
	}{
		{
			name:      "valid signature",
			signature: validSig,
			wantErr:   false,
		},
		{
			name:      "invalid signature",
			signature: "sha256=invalid",
			wantErr:   true,
		},
		{
			name:      "missing prefix",
			signature: computeHMAC(payload, secret),
			wantErr:   true,
		},
		{
			name:      "empty signature",
			signature: "",
			wantErr:   true,
		},
		{
			name:      "wrong prefix",
			signature: "sha1=" + computeHMAC(payload, secret),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGitHubSignature(payload, tt.signature, secret)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateGitHubSignature() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateLinearSignature(t *testing.T) {
	secret := []byte("linear-secret")
	payload := []byte(`{"action":"create","data":{"id":"123"}}`)
	validSig := computeHMAC(payload, secret)

	tests := []struct {
		name      string
		signature string
		wantErr   bool
	}{
		{
			name:      "valid signature",
			signature: validSig,
			wantErr:   false,
		},
		{
			name:      "invalid signature",
			signature: "invalid",
			wantErr:   true,
		},
		{
			name:      "empty signature",
			signature: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateLinearSignature(payload, tt.signature, secret)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateLinearSignature() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGenericTokenEnvName(t *testing.T) {
	tests := map[string]string{
		"alertmanager":       "ALERTMANAGER_WEBHOOK_TOKEN",
		"frontline-research": "FRONTLINE_RESEARCH_WEBHOOK_TOKEN",
	}
	for source, want := range tests {
		if got := GenericTokenEnvName(source); got != want {
			t.Errorf("GenericTokenEnvName(%q) = %q, want %q", source, got, want)
		}
	}
}

func TestValidateBearerToken(t *testing.T) {
	token := []byte("s3cret-token")

	tests := []struct {
		name          string
		authorization string
		token         []byte
		wantErr       bool
	}{
		{name: "matching token", authorization: "Bearer s3cret-token", token: token},
		{name: "scheme is case-insensitive", authorization: "bearer s3cret-token", token: token},
		{name: "wrong token", authorization: "Bearer other", token: token, wantErr: true},
		{name: "token prefix only", authorization: "Bearer s3cret", token: token, wantErr: true},
		{name: "missing header", authorization: "", token: token, wantErr: true},
		{name: "basic scheme", authorization: "Basic s3cret-token", token: token, wantErr: true},
		{name: "bare token without scheme", authorization: "s3cret-token", token: token, wantErr: true},
		{name: "empty configured token", authorization: "Bearer ", token: nil, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBearerToken(tt.authorization, tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBearerToken(%q) error = %v, wantErr %v", tt.authorization, err, tt.wantErr)
			}
		})
	}
}
