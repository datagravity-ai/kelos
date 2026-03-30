package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func computeHMACSHA256(payload, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestValidateGitHubSignature(t *testing.T) {
	secret := []byte("test-secret")
	payload := []byte(`{"action":"opened"}`)
	validSig := "sha256=" + computeHMACSHA256(payload, secret)

	tests := []struct {
		name    string
		sig     string
		secret  []byte
		wantErr bool
	}{
		{
			name:   "valid signature",
			sig:    validSig,
			secret: secret,
		},
		{
			name:    "wrong signature",
			sig:     "sha256=0000000000000000000000000000000000000000000000000000000000000000",
			secret:  secret,
			wantErr: true,
		},
		{
			name:    "missing prefix",
			sig:     computeHMACSHA256(payload, secret),
			secret:  secret,
			wantErr: true,
		},
		{
			name:    "empty secret",
			sig:     validSig,
			secret:  []byte{},
			wantErr: true,
		},
		{
			name:    "wrong secret",
			sig:     validSig,
			secret:  []byte("wrong-secret"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGitHubSignature(payload, tt.sig, tt.secret)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateGitHubSignature() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateLinearSignature(t *testing.T) {
	secret := []byte("linear-secret")
	payload := []byte(`{"action":"create","type":"Issue"}`)
	validSig := computeHMACSHA256(payload, secret)

	tests := []struct {
		name    string
		sig     string
		secret  []byte
		wantErr bool
	}{
		{
			name:   "valid signature",
			sig:    validSig,
			secret: secret,
		},
		{
			name:    "wrong signature",
			sig:     "0000000000000000000000000000000000000000000000000000000000000000",
			secret:  secret,
			wantErr: true,
		},
		{
			name:    "empty secret",
			sig:     validSig,
			secret:  []byte{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateLinearSignature(payload, tt.sig, tt.secret)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateLinearSignature() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
