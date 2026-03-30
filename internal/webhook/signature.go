package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// ValidateGitHubSignature validates a GitHub webhook HMAC-SHA256 signature.
// The signature header value should be in the form "sha256=<hex>".
func ValidateGitHubSignature(payload []byte, signatureHeader string, secret []byte) error {
	if len(secret) == 0 {
		return fmt.Errorf("webhook secret is empty")
	}

	sig := strings.TrimPrefix(signatureHeader, "sha256=")
	if sig == signatureHeader {
		return fmt.Errorf("signature header missing sha256= prefix")
	}

	expected, err := hex.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("decoding signature hex: %w", err)
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	computed := mac.Sum(nil)

	if !hmac.Equal(computed, expected) {
		return fmt.Errorf("signature mismatch")
	}

	return nil
}

// ValidateLinearSignature validates a Linear webhook signature.
// Linear uses a raw HMAC-SHA256 hex digest without a prefix.
func ValidateLinearSignature(payload []byte, signatureHeader string, secret []byte) error {
	if len(secret) == 0 {
		return fmt.Errorf("webhook secret is empty")
	}

	expected, err := hex.DecodeString(signatureHeader)
	if err != nil {
		return fmt.Errorf("decoding signature hex: %w", err)
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	computed := mac.Sum(nil)

	if !hmac.Equal(computed, expected) {
		return fmt.Errorf("signature mismatch")
	}

	return nil
}
