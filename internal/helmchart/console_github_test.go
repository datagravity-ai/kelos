package helmchart

import (
	"strings"
	"testing"

	"github.com/kelos-dev/kelos/internal/manifests"
)

func consoleGitHubValues() map[string]interface{} {
	console := consoleOIDCValues()
	auth := console["auth"].(map[string]interface{})
	provider := auth["oidc"].(map[string]interface{})
	delete(provider, "issuerURL")
	delete(auth, "oidc")
	auth["mode"], auth["github"] = "github", provider
	provider["usernamePrefix"], provider["groupsPrefix"] = "github:company:", "github:company:"
	return console
}

func TestRenderConsoleGitHub(t *testing.T) {
	for _, enterpriseURL := range []string{"", "https://github.company.example", "https://github.company.example:8443/"} {
		t.Run(enterpriseURL, func(t *testing.T) {
			console := consoleGitHubValues()
			config := console["auth"].(map[string]interface{})["github"].(map[string]interface{})
			config["org"] = "company"
			if enterpriseURL != "" {
				config["enterpriseURL"] = enterpriseURL
				config["caSecretName"] = "ghes-ca"
			}
			deployment, provider := testRenderConsoleProxy(t, "github", console)
			if provider["scope"] != "user:email read:org" || provider["clientID"] != "kelos-console" || provider["clientSecretFile"] != "/var/run/secrets/oauth2-proxy/client-secret" {
				t.Fatalf("GitHub client = %#v", provider)
			}
			if provider["oidcConfig"] != nil || provider["code_challenge_method"] != nil {
				t.Fatalf("GitHub has OIDC settings: %#v", provider)
			}
			if provider["githubConfig"].(map[string]interface{})["org"] != "company" {
				t.Fatalf("GitHub org = %#v", provider["githubConfig"])
			}
			for key, path := range map[string]string{"loginURL": "/login/oauth/authorize", "redeemURL": "/login/oauth/access_token", "validateURL": "/api/v3"} {
				if enterpriseURL == "" {
					if provider[key] != nil {
						t.Errorf("GitHub.com overrides %s: %v", key, provider[key])
					}
				} else if provider[key] != strings.TrimSuffix(enterpriseURL, "/")+path {
					t.Errorf("%s = %v", key, provider[key])
				}
			}
			if enterpriseURL == "" {
				if provider["caFiles"] != nil {
					t.Fatalf("unexpected CA files: %v", provider["caFiles"])
				}
				return
			}
			caFiles := provider["caFiles"].([]interface{})
			if len(caFiles) != 1 || caFiles[0] != "/var/run/secrets/oauth2-proxy-ca/ca.crt" || provider["useSystemTrustStore"] != true {
				t.Fatalf("CA configuration = %#v", provider)
			}
			foundMount, foundVolume := false, false
			for _, mount := range deployment.Spec.Template.Spec.Containers[1].VolumeMounts {
				if mount.Name == "proxy-ca" {
					foundMount = mount.ReadOnly && mount.MountPath == "/var/run/secrets/oauth2-proxy-ca"
				}
			}
			for _, volume := range deployment.Spec.Template.Spec.Volumes {
				if volume.Name == "proxy-ca" && volume.Secret != nil {
					foundVolume = volume.Secret.SecretName == "ghes-ca" && len(volume.Secret.Items) == 1 && volume.Secret.Items[0].Key == "ca.crt" && volume.Secret.Items[0].Path == "ca.crt"
				}
			}
			if !foundMount || !foundVolume {
				t.Fatalf("CA Secret is not mounted: %#v", deployment.Spec.Template.Spec)
			}
		})
	}
}

func TestRenderConsoleGitHubRejectsInvalidConfiguration(t *testing.T) {
	for _, endpoint := range []string{"http://github.example", "https://", "https://github.example/api/v3", "https://github.example?token=x", "https://github.example?", "https://github.example#", "https://github.example/#fragment", "https://user:password@github.example", "https://github.example/ bad", "https://github.example:bad"} {
		t.Run(endpoint, func(t *testing.T) {
			console := consoleGitHubValues()
			console["auth"].(map[string]interface{})["github"].(map[string]interface{})["enterpriseURL"] = endpoint
			if _, err := Render(manifests.ChartFS, map[string]interface{}{"consoleServer": console}); err == nil {
				t.Fatalf("invalid enterpriseURL accepted: %s", endpoint)
			}
		})
	}
	for _, test := range []struct {
		name   string
		change func(map[string]interface{}, map[string]interface{})
	}{
		{"missing client", func(c, g map[string]interface{}) { delete(g, "clientID") }},
		{"missing secret", func(c, g map[string]interface{}) { delete(g, "secretName") }},
		{"missing prefix", func(c, g map[string]interface{}) { delete(g, "usernamePrefix") }},
		{"reserved prefix", func(c, g map[string]interface{}) { g["groupsPrefix"] = "system:" }},
		{"HTTP callback", func(c, g map[string]interface{}) { g["redirectURL"] = "http://console.example/oauth2/callback" }},
		{"callback path", func(c, g map[string]interface{}) { g["redirectURL"] = "https://console.example/other" }},
		{"static secret", func(c, g map[string]interface{}) { c["secretName"] = "static" }},
		{"static mode", func(c, g map[string]interface{}) {
			c["secretName"] = "static"
			c["auth"].(map[string]interface{})["mode"] = "staticToken"
		}},
		{"OIDC settings", func(c, g map[string]interface{}) {
			c["auth"].(map[string]interface{})["oidc"] = map[string]interface{}{"issuerURL": "https://issuer.example"}
		}},
		{"unknown settings", func(c, g map[string]interface{}) { g["loginURL"] = "https://github.example" }},
		{"proxy without trusted IPs", func(c, g map[string]interface{}) { delete(g, "trustedProxyIPs") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			console := consoleGitHubValues()
			github := console["auth"].(map[string]interface{})["github"].(map[string]interface{})
			test.change(console, github)
			if _, err := Render(manifests.ChartFS, map[string]interface{}{"consoleServer": console}); err == nil {
				t.Fatal("invalid GitHub configuration accepted")
			}
		})
	}
}
