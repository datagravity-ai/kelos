package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
)

// ValidateGitHubSignature validates a GitHub webhook signature.
// GitHub sends signatures in the format "sha256=<hex-digest>".
func ValidateGitHubSignature(payload []byte, signature string, secret []byte) error {
	if signature == "" {
		return fmt.Errorf("missing signature")
	}

	// GitHub signature format: "sha256=<hex-digest>"
	if !strings.HasPrefix(signature, "sha256=") {
		return fmt.Errorf("invalid signature format: expected sha256= prefix")
	}

	expectedSig := signature[7:] // Remove "sha256=" prefix
	return validateHMACSignature(payload, expectedSig, secret)
}

// ValidateLinearSignature validates a Linear webhook signature.
// Linear sends signatures as raw HMAC-SHA256 hex digest.
func ValidateLinearSignature(payload []byte, signature string, secret []byte) error {
	if signature == "" {
		return fmt.Errorf("missing signature")
	}

	return validateHMACSignature(payload, signature, secret)
}

// validateHMACSignature performs HMAC-SHA256 validation against the expected hex digest.
func validateHMACSignature(payload []byte, expectedSig string, secret []byte) error {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	actualSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(actualSig), []byte(expectedSig)) {
		return fmt.Errorf("signature verification failed")
	}

	return nil
}

// GenericTokenEnvName returns the environment variable that holds the bearer
// token for a generic webhook source: the source name upper-cased, with "-"
// replaced by "_", followed by "_WEBHOOK_TOKEN".
func GenericTokenEnvName(source string) string {
	return strings.ToUpper(strings.ReplaceAll(source, "-", "_")) + "_WEBHOOK_TOKEN"
}

// ValidateBearerToken checks an Authorization header of the form
// "Bearer <token>" against the expected token in constant time.
func ValidateBearerToken(authorization string, token []byte) error {
	if len(token) == 0 {
		return fmt.Errorf("no token configured")
	}
	scheme, credential, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return fmt.Errorf("missing bearer token")
	}
	if subtle.ConstantTimeCompare([]byte(credential), token) != 1 {
		return fmt.Errorf("bearer token mismatch")
	}
	return nil
}
