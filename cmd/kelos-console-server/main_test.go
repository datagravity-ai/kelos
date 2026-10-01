package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelos-dev/kelos/internal/consoleserver"
)

func TestReadToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := readToken(path)
	if err != nil {
		t.Fatalf("readToken() error = %v", err)
	}
	if value != "secret" {
		t.Fatalf("readToken() = %q, want %q", value, "secret")
	}
}

func TestValidateAuthFlags(t *testing.T) {
	for _, test := range []struct {
		mode, address, token string
		secure, valid        bool
	}{
		{"staticToken", ":8080", "token", false, true},
		{"staticToken", ":8080", "", false, false},
		{"oidc", "127.0.0.1:8080", "", false, true},
		{"oidc", "[::1]:8080", "", false, true},
		{"oidc", ":8080", "", false, false},
		{"oidc", "0.0.0.0:8080", "", false, false},
		{"oidc", "localhost:8080", "", false, false},
		{"oidc", "127.0.0.1:8080", "token", false, false},
		{"oidc", "127.0.0.1:8080", "", true, false},
		{"unknown", ":8080", "token", false, false},
	} {
		err := validateAuthFlags(test.mode, test.address, test.token, test.secure, consoleserver.OIDCConfig{})
		if (err == nil) != test.valid {
			t.Errorf("%#v: error = %v", test, err)
		}
	}
	for _, oidc := range []consoleserver.OIDCConfig{
		{ExternalURL: "https://console.example"},
		{UsernamePrefix: "oidc:"},
		{GroupsPrefix: "oidc:"},
	} {
		if err := validateAuthFlags("staticToken", ":8080", "token", false, oidc); err == nil {
			t.Fatalf("static mode accepted OIDC flags: %#v", oidc)
		}
	}
}

func TestReadTokenRejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readToken(path); err == nil {
		t.Fatal("readToken() error = nil, want non-nil")
	}
}

func TestReadTokenRejectsWhitespaceOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(" \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readToken(path); err == nil {
		t.Fatal("readToken() error = nil, want non-nil")
	}
}
