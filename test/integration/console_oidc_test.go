package integration

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/consoleserver"
	"github.com/kelos-dev/kelos/internal/helmchart"
	"github.com/kelos-dev/kelos/internal/manifests"
)

// TestConsoleOIDC exercises the rendered proxy configuration, real OIDC callbacks,
// and Kubernetes RBAC without depending on an external identity provider.
func TestConsoleOIDC(t *testing.T) {
	proxyBinary := os.Getenv("OAUTH2_PROXY_BIN")
	if proxyBinary == "" {
		t.Fatal("OAUTH2_PROXY_BIN is required; run make test-integration")
	}
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "internal", "manifests")}, ErrorIfCRDPathMissing: true}
	environment.ControlPlane.GetAPIServer().Configure().Set("authorization-mode", "RBAC")
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	config.QPS, config.Burst = 100, 100
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, team := range []string{"a", "b"} {
		namespace := "team-" + team
		for _, object := range []client.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
			&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "console", Namespace: namespace}, Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{"kelos.dev"}, Resources: []string{"sessions", "workspaces", "agentconfigs", "taskpipelines", "tasks"}, Verbs: []string{"get", "list"}},
				{APIGroups: []string{"kelos.dev"}, Resources: []string{"sessions"}, Verbs: []string{"create", "patch", "delete"}},
				{APIGroups: []string{"kelos.dev"}, Resources: []string{"sessions/suspend", "sessions/resume", "sessions/connect"}, Verbs: []string{"create"}},
			}},
			&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "console", Namespace: namespace}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "console"}, Subjects: []rbacv1.Subject{{APIGroup: "rbac.authorization.k8s.io", Kind: "Group", Name: "oidc:developers-" + team}}},
		} {
			if err := kube.Create(ctx, object); err != nil {
				t.Fatal(err)
			}
		}
	}

	issuer := newConsoleTestIssuer(t)
	proxyAddress := unusedConsoleAddress(t)
	proxyURL, _ := url.Parse("http://" + proxyAddress)
	edgeProxy := httputil.NewSingleHostReverseProxy(proxyURL)
	director := edgeProxy.Director
	edgeProxy.Director = func(r *http.Request) {
		director(r)
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", r.Host)
	}
	edgeProxy.ErrorLog = log.New(io.Discard, "", 0)
	edge := httptest.NewTLSServer(edgeProxy)
	t.Cleanup(edge.Close)
	server, err := consoleserver.New(consoleserver.Config{
		AuthMode: consoleserver.AuthModeOIDC,
		OIDC:     &consoleserver.OIDCConfig{ExternalURL: edge.URL, UsernamePrefix: "oidc:", GroupsPrefix: "oidc:", Reviewer: clientset.AuthorizationV1().SubjectAccessReviews(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))},
		Client:   kube, Clientset: clientset, RESTConfig: config, DefaultNamespace: "team-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	var headersMu sync.Mutex
	var forwarded []http.Header
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headersMu.Lock()
		forwarded = append(forwarded, r.Header.Clone())
		headersMu.Unlock()
		server.ServeHTTP(w, r)
	}))
	t.Cleanup(backend.Close)
	directory := t.TempDir()
	caPath := filepath.Join(directory, "issuer-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuer.server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(directory, "client-secret")
	if err := os.WriteFile(secretPath, []byte("test-client-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	proxyArgs, proxyHeaders := renderConsoleTestProxy(t, issuer.server.URL, edge.URL)
	headerPath := filepath.Join(directory, "oauth2-proxy.yaml")
	var proxyConfig map[string]interface{}
	if err := yaml.Unmarshal([]byte(proxyHeaders), &proxyConfig); err != nil {
		t.Fatal(err)
	}
	proxyConfig["server"].(map[string]interface{})["bindAddress"] = proxyAddress
	upstream := proxyConfig["upstreamConfig"].(map[string]interface{})["upstreams"].([]interface{})[0].(map[string]interface{})
	upstream["uri"] = backend.URL + "/"
	provider := proxyConfig["providers"].([]interface{})[0].(map[string]interface{})
	provider["clientSecretFile"] = secretPath
	provider["caFiles"] = []string{caPath}
	configYAML, err := yaml.Marshal(proxyConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(headerPath, configYAML, 0600); err != nil {
		t.Fatal(err)
	}
	for i, arg := range proxyArgs {
		if strings.HasPrefix(arg, "--alpha-config=") {
			proxyArgs[i] = "--alpha-config=" + headerPath
		}
	}
	logPath := filepath.Join(directory, "proxy.log")
	proxyLog, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, proxyBinary, proxyArgs...)
	command.Env = append(os.Environ(), "OAUTH2_PROXY_COOKIE_SECRET="+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{251, 255}, 16)))
	command.Stdout, command.Stderr = proxyLog, proxyLog
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	proxyDone := make(chan struct{})
	var proxyError error
	go func() { proxyError = command.Wait(); close(proxyDone) }()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		<-proxyDone
		_ = proxyLog.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Logf("Proxy output:\n%s", data)
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		select {
		case <-proxyDone:
			t.Fatalf("proxy exited before readiness: %v", proxyError)
		default:
		}
		response, err := edge.Client().Get(edge.URL + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("proxy did not become healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}

	pool := x509.NewCertPool()
	pool.AddCert(edge.Certificate())
	pool.AddCert(issuer.server.Certificate())
	newBrowser := func() *http.Client {
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Client{Jar: jar, Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	}
	request := func(browser *http.Client, method, path, body string, spoof bool) (int, string) {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, edge.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", edge.URL)
		r.Header.Set("Content-Type", "application/json")
		if spoof {
			r.Header["X-Kelos-User"] = []string{"admin", "system:admin"}
			r.Header.Set("X_Kelos_Groups", "system:masters")
			r.Header.Set("X-Kelos-Groups", "developers-b")
			r.Header.Set("X-Forwarded-Access-Token", "caller-secret")
			r.Header.Set("Authorization", "Bearer caller-secret")
		}
		response, err := browser.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(data)
	}
	anonymous := newBrowser()
	if status, _ := request(anonymous, "GET", "/api/sessions", "", true); status != 401 {
		t.Fatalf("forged anonymous identity status = %d", status)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		if status, _ := request(anonymous, "GET", path, "", true); status != 200 {
			t.Fatalf("probe %s = %d", path, status)
		}
	}
	alice, bob := newBrowser(), newBrowser()
	for _, identity := range []struct {
		user, group string
		browser     *http.Client
	}{{"alice", "developers-a", alice}, {"bob", "developers-b", bob}} {
		issuer.setIdentity(identity.user, identity.group)
		if status, body := request(identity.browser, "GET", "/oauth2/start?rd=/", "", false); status != 200 {
			t.Fatalf("login %s = %d %s", identity.user, status, body)
		}
		for _, team := range []string{"a", "b"} {
			want := 403
			if identity.group == "developers-"+team {
				want = 200
			}
			for _, path := range []string{"/api/sessions?namespace=team-", "/api/resources?namespace=team-"} {
				if status, body := request(identity.browser, "GET", path+team, "", true); status != want {
					t.Fatalf("%s %s%s = %d %s, want %d", identity.user, path, team, status, body, want)
				}
			}
		}
	}
	if status, body := request(alice, "GET", "/api/config", "", true); status != 200 || !strings.Contains(body, `"username":"oidc:alice"`) {
		t.Fatalf("verified principal = %d %s", status, body)
	}
	manifest := `{"name":"chat","namespace":"team-a","worker":{"type":"codex","credentials":{"type":"none"},"workspaceRef":{"name":"workspace-a"}}}`
	if status, body := request(alice, "POST", "/api/sessions", manifest, false); status != 201 {
		t.Fatalf("create = %d %s", status, body)
	}
	if status, body := request(alice, "GET", "/api/resources?namespace=team-a", "", false); status != 200 || !strings.Contains(body, `"relationship":"uses"`) || !strings.Contains(body, `"name":"workspace-a"`) {
		t.Fatalf("inventory relationships = %d %s", status, body)
	}
	if status, body := request(alice, "POST", "/api/sessions/team-a/chat/suspend", "", false); status != 202 {
		t.Fatalf("suspend = %d %s", status, body)
	}
	var session kelos.Session
	if err := kube.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "chat"}, &session); err != nil {
		t.Fatal(err)
	}
	if session.Spec.Suspend == nil || !*session.Spec.Suspend {
		t.Fatal("suspend was not applied")
	}
	if status, body := request(alice, "POST", "/api/sessions/team-a/chat/resume", "", false); status != 202 {
		t.Fatalf("resume = %d %s", status, body)
	}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "chat"}, &session); err != nil {
		t.Fatal(err)
	}
	if session.Spec.Suspend != nil && *session.Spec.Suspend {
		t.Fatal("resume was not applied")
	}
	session.Status.Phase = kelos.SessionPhaseReady
	session.Status.PodName = "chat-pod"
	if err := kube.Status().Update(ctx, &session); err != nil {
		t.Fatal(err)
	}
	connect := func(browser *http.Client, endpoint string, want int) {
		t.Helper()
		dialer := websocket.Dialer{Jar: browser.Jar, TLSClientConfig: &tls.Config{RootCAs: pool}, HandshakeTimeout: 5 * time.Second}
		header := http.Header{"Origin": {edge.URL}, "X-Kelos-User": {"admin"}, "X-Kelos-Groups": {"developers-a"}}
		target := "wss" + strings.TrimPrefix(edge.URL, "https") + "/api/sessions/team-a/chat/" + endpoint
		connection, response, err := dialer.DialContext(ctx, target, header)
		if response == nil || response.StatusCode != want {
			t.Fatalf("%s handshake = %v, error = %v, want %d", endpoint, response, err, want)
		}
		defer response.Body.Close()
		if connection != nil {
			defer connection.Close()
		}
		if want == http.StatusSwitchingProtocols {
			if err != nil || connection == nil {
				t.Fatalf("%s upgrade failed: %v", endpoint, err)
			}
			// Envtest has no kubelet; the stream reaches the Pod API after the authenticated upgrade.
			_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
			var event map[string]interface{}
			if err := connection.ReadJSON(&event); err != nil || event["type"] != "error" {
				t.Fatalf("%s stream without a kubelet = %v, error = %v", endpoint, event, err)
			}
		}
	}
	for _, endpoint := range []string{"connect", "exec"} {
		for range 2 {
			connect(alice, endpoint, http.StatusSwitchingProtocols)
		}
		connect(bob, endpoint, http.StatusForbidden)
	}

	// RBAC revocation is independent of the still-valid proxy session.
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "console", Namespace: "team-a"}}
	if err := kube.Delete(ctx, binding); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		status, body := request(alice, "GET", "/api/sessions?namespace=team-a", "", false)
		if status == 403 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("revoked access = %d %s", status, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status, _ := request(alice, "GET", "/api/config", "", false); status != 200 {
		t.Fatalf("revocation incorrectly ended authentication: %d", status)
	}
	for _, endpoint := range []string{"connect", "exec"} {
		connect(alice, endpoint, http.StatusForbidden)
	}
	if status, body := request(alice, "POST", "/api/logout", "", false); status != 200 || !strings.Contains(body, "sign_out") {
		t.Fatalf("logout = %d %s", status, body)
	}
	if status, _ := request(alice, "GET", "/oauth2/sign_out?rd=/oauth2/sign_in", "", false); status != 200 {
		t.Fatalf("proxy logout = %d", status)
	}
	if status, _ := request(alice, "GET", "/api/config", "", false); status != 401 {
		t.Fatalf("signed-out API status = %d", status)
	}
	headersMu.Lock()
	defer headersMu.Unlock()
	for _, header := range forwarded {
		for _, name := range []string{"Cookie", "Authorization", "X-Forwarded-Access-Token", "X_Kelos_Groups"} {
			if header.Get(name) != "" {
				t.Errorf("proxy forwarded %s", name)
			}
		}
	}
}

func unusedConsoleAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func renderConsoleTestProxy(t *testing.T, issuerURL, externalURL string) ([]string, string) {
	t.Helper()
	data, err := helmchart.Render(manifests.ChartFS, map[string]interface{}{"crds": map[string]interface{}{"install": false}, "consoleServer": map[string]interface{}{
		"enabled": true, "auth": map[string]interface{}{"mode": "oidc", "oidc": map[string]interface{}{
			"issuerURL": issuerURL, "clientID": "kelos-console", "redirectURL": externalURL + "/oauth2/callback", "secretName": "oidc",
			"usernamePrefix": "oidc:", "groupsPrefix": "oidc:",
			"reverseProxy": true, "trustedProxyIPs": []interface{}{"127.0.0.1"},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	var headers string
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var object unstructured.Unstructured
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if object.GetKind() == "Deployment" && object.GetName() == "kelos-console-server" {
			var deployment appsv1.Deployment
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &deployment); err != nil {
				t.Fatal(err)
			}
			for _, container := range deployment.Spec.Template.Spec.Containers {
				if container.Name == "oauth2-proxy" {
					args = container.Args
				}
			}
		}
		if object.GetKind() == "ConfigMap" && object.GetName() == "kelos-console-oidc" {
			headers, _, _ = unstructured.NestedString(object.Object, "data", "oauth2-proxy.yaml")
		}
	}
	if len(args) == 0 || headers == "" {
		t.Fatal("rendered proxy configuration missing")
	}
	return args, headers
}

type consoleTestIssuer struct {
	server      *httptest.Server
	mu          sync.Mutex
	user, group string
	codes       map[string]map[string]string
}

func (issuer *consoleTestIssuer) setIdentity(user, group string) {
	issuer.mu.Lock()
	defer issuer.mu.Unlock()
	issuer.user, issuer.group = user, group
}

func newConsoleTestIssuer(t *testing.T) *consoleTestIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &consoleTestIssuer{codes: map[string]map[string]string{}}
	write := func(w http.ResponseWriter, value interface{}) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	issuer.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			write(w, map[string]interface{}{"issuer": issuer.server.URL, "authorization_endpoint": issuer.server.URL + "/authorize", "token_endpoint": issuer.server.URL + "/token", "jwks_uri": issuer.server.URL + "/keys", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}})
		case "/keys":
			write(w, map[string]interface{}{"keys": []interface{}{map[string]string{"kty": "RSA", "kid": "test", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/authorize":
			query := r.URL.Query()
			if query.Get("code_challenge_method") != "S256" || query.Get("nonce") == "" {
				http.Error(w, "PKCE and nonce are required", 400)
				return
			}
			issuer.mu.Lock()
			code := fmt.Sprintf("code-%d", len(issuer.codes))
			issuer.codes[code] = map[string]string{"user": issuer.user, "group": issuer.group, "nonce": query.Get("nonce"), "challenge": query.Get("code_challenge")}
			issuer.mu.Unlock()
			callback, err := url.Parse(query.Get("redirect_uri"))
			if err != nil {
				http.Error(w, "bad redirect", 400)
				return
			}
			values := callback.Query()
			values.Set("code", code)
			values.Set("state", query.Get("state"))
			callback.RawQuery = values.Encode()
			http.Redirect(w, r, callback.String(), http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", 400)
				return
			}
			issuer.mu.Lock()
			claims, ok := issuer.codes[r.Form.Get("code")]
			issuer.mu.Unlock()
			challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || base64.RawURLEncoding.EncodeToString(challenge[:]) != claims["challenge"] {
				http.Error(w, "invalid code verifier", 400)
				return
			}
			payload, _ := json.Marshal(map[string]interface{}{"iss": issuer.server.URL, "sub": claims["user"], "aud": "kelos-console", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": claims["nonce"], "email": claims["user"] + "@example.com", "email_verified": true, "groups": []string{claims["group"], "system:masters"}})
			token := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload)
			digest := sha256.Sum256([]byte(token))
			signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
			if err != nil {
				http.Error(w, "signing failed", 500)
				return
			}
			write(w, map[string]interface{}{"access_token": "provider-access-secret", "token_type": "Bearer", "expires_in": 3600, "id_token": token + "." + base64.RawURLEncoding.EncodeToString(signature)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(issuer.server.Close)
	return issuer
}
