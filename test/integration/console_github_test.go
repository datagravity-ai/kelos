package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newConsoleTestGitHub(t *testing.T) *consoleTestProvider {
	t.Helper()
	provider := &consoleTestProvider{codes: map[string]map[string]string{}}
	write := func(w http.ResponseWriter, value interface{}) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	provider.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/authorize":
			query := r.URL.Query()
			if query.Get("client_id") != "kelos-console" || query.Get("scope") != "user:email read:org" || query.Get("state") == "" {
				http.Error(w, "invalid authorization request", http.StatusBadRequest)
				return
			}
			provider.mu.Lock()
			code := fmt.Sprintf("code-%d", len(provider.codes))
			provider.codes[code] = map[string]string{"user": provider.user, "group": provider.group}
			provider.mu.Unlock()
			callback, err := url.Parse(query.Get("redirect_uri"))
			if err != nil || callback.Path != "/oauth2/callback" {
				http.Error(w, "invalid callback", http.StatusBadRequest)
				return
			}
			values := callback.Query()
			values.Set("code", code)
			values.Set("state", query.Get("state"))
			callback.RawQuery = values.Encode()
			http.Redirect(w, r, callback.String(), http.StatusFound)
		case "/login/oauth/access_token":
			if err := r.ParseForm(); err != nil || r.Form.Get("client_id") != "kelos-console" || r.Form.Get("client_secret") != "test-client-secret" {
				http.Error(w, "invalid client", http.StatusUnauthorized)
				return
			}
			provider.mu.Lock()
			_, ok := provider.codes[r.Form.Get("code")]
			provider.mu.Unlock()
			if !ok {
				http.Error(w, "invalid code", http.StatusBadRequest)
				return
			}
			write(w, map[string]interface{}{"access_token": "token-" + r.Form.Get("code"), "token_type": "bearer", "scope": "user:email read:org"})
		default:
			code := strings.TrimPrefix(r.Header.Get("Authorization"), "token token-")
			provider.mu.Lock()
			identity, ok := provider.codes[code]
			provider.mu.Unlock()
			if !ok {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
			org := "kelos"
			if identity["group"] == "outside" {
				org = "other"
			}
			switch r.URL.Path {
			case "/api/v3", "/api/v3/user":
				write(w, map[string]interface{}{"id": 42, "login": identity["user"], "email": identity["user"] + "@example.com"})
			case "/api/v3/user/emails":
				write(w, []map[string]interface{}{{"email": identity["user"] + "@example.com", "primary": true, "verified": true}})
			case "/api/v3/user/orgs", "/api/v3/user/teams":
				if r.URL.Query().Get("page") != "1" {
					write(w, []interface{}{})
				} else if r.URL.Path == "/api/v3/user/orgs" {
					write(w, []map[string]string{{"login": org}})
				} else {
					write(w, []map[string]interface{}{{"slug": identity["group"], "organization": map[string]string{"login": org}}})
				}
			default:
				http.NotFound(w, r)
			}
		}
	}))
	t.Cleanup(provider.server.Close)
	return provider
}
