package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
			switch r.URL.Path {
			case "/api/v3", "/api/v3/user":
				write(w, map[string]interface{}{"id": 42, "login": identity["user"], "email": identity["user"] + "@example.com"})
			case "/api/v3/user/emails":
				write(w, []map[string]interface{}{{"email": identity["user"] + "@example.com", "primary": true, "verified": true}})
			case "/api/v3/user/orgs", "/api/v3/user/teams":
				items := []interface{}{}
				for _, group := range consoleTestGitHubGroups(identity["group"]) {
					org, team, isTeam := strings.Cut(group, ":")
					if r.URL.Path == "/api/v3/user/teams" && isTeam {
						items = append(items, map[string]interface{}{"slug": team, "organization": map[string]string{"login": org}})
					} else if r.URL.Path == "/api/v3/user/orgs" && !isTeam {
						items = append(items, map[string]string{"login": org})
					}
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
				if page < 1 || perPage != 100 {
					http.Error(w, "invalid pagination", http.StatusBadRequest)
					return
				}
				start := min((page-1)*perPage, len(items))
				write(w, items[start:min(start+perPage, len(items))])
			default:
				http.NotFound(w, r)
			}
		}
	}))
	t.Cleanup(provider.server.Close)
	return provider
}

func consoleTestGitHubGroups(group string) []string {
	org := "kelos"
	if group == "outside" {
		org = "other"
	}
	var groups []string
	for i := range 207 {
		groups = append(groups, fmt.Sprintf("engineering-organization-%03d", i))
	}
	groups = append(groups, org)
	for i := range 203 {
		groups = append(groups, fmt.Sprintf("engineering-organization-%03d:developers", i))
	}
	groups = append(groups, org+":"+group)
	if group != "visitors" {
		groups = append(groups, org+":console-users")
	}
	return groups
}
