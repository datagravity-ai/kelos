package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateGitHubSignature_ValidSignature(t *testing.T) {
	// Set up environment
	t.Setenv("GITHUB_WEBHOOK_SECRET", "test-secret")

	payload := []byte(`{"action":"opened"}`)

	// Compute expected signature
	// echo -n '{"action":"opened"}' | openssl dgst -sha256 -hmac 'test-secret'
	// Result: sha256=d5d8634ca6bb8fcf239a9c24d47f5c90f96cf45b9b4475d5e32f1e6f3e6e8b14
	expectedSig := "sha256=d5d8634ca6bb8fcf239a9c24d47f5c90f96cf45b9b4475d5e32f1e6f3e6e8b14"

	headers := http.Header{}
	headers.Set("X-Hub-Signature-256", expectedSig)

	err := validateGitHubSignature(headers, payload)
	if err != nil {
		t.Errorf("Expected valid signature, got error: %v", err)
	}
}

func TestValidateGitHubSignature_InvalidSignature(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "test-secret")

	payload := []byte(`{"action":"opened"}`)

	headers := http.Header{}
	headers.Set("X-Hub-Signature-256", "sha256=wrongsignature")

	err := validateGitHubSignature(headers, payload)
	if err == nil {
		t.Error("Expected error for invalid signature, got nil")
	}
}

func TestValidateGitHubSignature_MissingHeader(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "test-secret")

	payload := []byte(`{"action":"opened"}`)
	headers := http.Header{}

	err := validateGitHubSignature(headers, payload)
	if err == nil {
		t.Error("Expected error for missing signature header, got nil")
	}
}

func TestValidateGitHubSignature_NoSecretConfigured(t *testing.T) {
	// Don't set GITHUB_WEBHOOK_SECRET - should skip validation
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")

	payload := []byte(`{"action":"opened"}`)
	headers := http.Header{}
	headers.Set("X-Hub-Signature-256", "sha256=anysignature")

	err := validateGitHubSignature(headers, payload)
	if err != nil {
		t.Errorf("Expected no error when secret not configured, got: %v", err)
	}
}

func TestWebhookHandler_MethodNotAllowed(t *testing.T) {
	handler := &webhookHandler{}

	req := httptest.NewRequest(http.MethodGet, "/webhook/github", nil)
	w := httptest.NewRecorder()

	handler.handle(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
}

func TestWebhookHandler_MissingSource(t *testing.T) {
	handler := &webhookHandler{}

	req := httptest.NewRequest(http.MethodPost, "/webhook/", nil)
	w := httptest.NewRecorder()

	handler.handle(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}
