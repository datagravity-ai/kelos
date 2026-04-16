package helmchart

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/kelos-dev/kelos/internal/manifests"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	sigyaml "sigs.k8s.io/yaml"
)

// imageLatestRefRE matches actual image references ending in ":latest" while
// ignoring narrative occurrences in CRD descriptions like "Defaults to Always
// if :latest tag is specified" — the leading non-whitespace requirement
// distinguishes "registry/name:latest" from " :latest" prose.
var imageLatestRefRE = regexp.MustCompile(`\S:latest`)

func TestRender_NilValues(t *testing.T) {
	data, err := Render(manifests.ChartFS, nil)
	if err != nil {
		t.Fatalf("rendering chart with nil values: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty rendered output")
	}
	output := string(data)
	for _, expected := range []string{
		"kind: CustomResourceDefinition",
		"kind: Namespace",
		"kind: ServiceAccount",
		"kind: ClusterRole",
		"kind: Deployment",
		"kind: CronJob",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected rendered output to contain %q", expected)
		}
	}
	if !imageLatestRefRE.MatchString(output) {
		t.Error("expected :latest image refs in rendered output when using default values")
	}
}

func TestRender_DefaultValues(t *testing.T) {
	vals := map[string]interface{}{
		"image": map[string]interface{}{
			"tag": "v0.0.0-test",
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty rendered output")
	}
	output := string(data)
	for _, expected := range []string{
		"kind: CustomResourceDefinition",
		"kind: Namespace",
		"kind: ServiceAccount",
		"kind: ClusterRole",
		"kind: Deployment",
		"kind: CronJob",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected rendered output to contain %q", expected)
		}
	}
}

func TestRender_VersionOverride(t *testing.T) {
	vals := map[string]interface{}{
		"image": map[string]interface{}{
			"tag": "v1.2.3",
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if imageLatestRefRE.MatchString(output) {
		t.Error("expected no :latest image refs in rendered output")
	}
	if !strings.Contains(output, ":v1.2.3") {
		t.Error("expected :v1.2.3 tags in rendered output")
	}
}

func TestRender_PullPolicy(t *testing.T) {
	vals := map[string]interface{}{
		"image": map[string]interface{}{
			"tag":        "latest",
			"pullPolicy": "IfNotPresent",
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "imagePullPolicy: IfNotPresent") {
		t.Error("expected imagePullPolicy: IfNotPresent in rendered output")
	}
}

func TestRender_DisableTelemetry(t *testing.T) {
	vals := map[string]interface{}{
		"telemetry": map[string]interface{}{
			"enabled": false,
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if strings.Contains(output, "kelos-telemetry") {
		t.Error("expected kelos-telemetry CronJob to be excluded")
	}
}

func TestRender_ResourceOrdering(t *testing.T) {
	data, err := Render(manifests.ChartFS, nil)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	// CRDs must appear before Namespace, and Namespace must appear before
	// Deployment and CronJob so that dependencies exist when resources are applied.
	crdIdx := strings.Index(output, "kind: CustomResourceDefinition")
	nsIdx := strings.Index(output, "kind: Namespace")
	deployIdx := strings.Index(output, "kind: Deployment")
	cronIdx := strings.Index(output, "kind: CronJob")
	if crdIdx < 0 || nsIdx < 0 || deployIdx < 0 || cronIdx < 0 {
		t.Fatal("expected CustomResourceDefinition, Namespace, Deployment, and CronJob in rendered output")
	}
	if crdIdx >= nsIdx {
		t.Error("expected CustomResourceDefinition to appear before Namespace")
	}
	if nsIdx >= deployIdx {
		t.Error("expected Namespace to appear before Deployment")
	}
	if nsIdx >= cronIdx {
		t.Error("expected Namespace to appear before CronJob")
	}
}

func TestRender_DisableCRDs(t *testing.T) {
	vals := map[string]interface{}{
		"crds": map[string]interface{}{
			"install": false,
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if strings.Contains(output, "kind: CustomResourceDefinition") {
		t.Error("expected no CRDs when crds.install is false")
	}
	if !strings.Contains(output, "kind: Namespace") {
		t.Error("expected Namespace to still be present")
	}
}

func TestRender_TaskSpawnerTemplatePlaceholdersRemainLiteral(t *testing.T) {
	data, err := Render(manifests.ChartFS, nil)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, `Supports Go text/template variables from the work item, e.g. "kelos-task-{{.Number}}".`) {
		t.Error("expected branch placeholder example to remain literal in rendered CRD output")
	}
	for _, expected := range []string{
		"Available variables (all sources): {{.ID}}, {{.Title}}, {{.Kind}}",
		"GitHub issue/Jira sources: {{.Number}}, {{.Body}}, {{.URL}}, {{.Labels}}, {{.Comments}}",
		"GitHub pull request sources additionally expose: {{.Branch}}, {{.ReviewState}}, {{.ReviewComments}}",
		"Cron sources: {{.Time}}, {{.Schedule}}",
	} {
		if count := strings.Count(output, expected); count != 2 {
			t.Errorf("expected %q to appear twice in TaskSpawner CRD descriptions, got %d", expected, count)
		}
	}
}

func TestRender_CRDKeepAnnotation(t *testing.T) {
	vals := map[string]interface{}{
		"crds": map[string]interface{}{
			"install": true,
			"keep":    true,
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "helm.sh/resource-policy") {
		t.Error("expected helm.sh/resource-policy annotation when crds.keep is true")
	}
}

func TestRender_CRDNoKeepAnnotation(t *testing.T) {
	vals := map[string]interface{}{
		"crds": map[string]interface{}{
			"install": true,
			"keep":    false,
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if strings.Contains(output, "helm.sh/resource-policy") {
		t.Error("expected no helm.sh/resource-policy annotation when crds.keep is false")
	}
}

func TestRender_LinearWebhookApiKeySecret(t *testing.T) {
	tests := []struct {
		name             string
		apiKeySecretName string
		wantEnvVar       bool
	}{
		{
			name:             "apiKeySecretName set injects LINEAR_API_KEY env var",
			apiKeySecretName: "my-linear-api-secret",
			wantEnvVar:       true,
		},
		{
			name:             "apiKeySecretName empty omits LINEAR_API_KEY env var",
			apiKeySecretName: "",
			wantEnvVar:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := map[string]interface{}{
				"webhookServer": map[string]interface{}{
					"sources": map[string]interface{}{
						"linear": map[string]interface{}{
							"enabled":          true,
							"replicas":         1,
							"secretName":       "linear-webhook-secret",
							"apiKeySecretName": tt.apiKeySecretName,
						},
					},
				},
			}
			data, err := Render(manifests.ChartFS, vals)
			if err != nil {
				t.Fatalf("rendering chart: %v", err)
			}
			output := string(data)
			if tt.wantEnvVar {
				if !strings.Contains(output, "LINEAR_API_KEY") {
					t.Error("expected LINEAR_API_KEY env var in rendered output")
				}
				if !strings.Contains(output, tt.apiKeySecretName) {
					t.Errorf("expected secret name %q in rendered output", tt.apiKeySecretName)
				}
			} else {
				if strings.Contains(output, "LINEAR_API_KEY") {
					t.Error("expected no LINEAR_API_KEY env var in rendered output")
				}
			}
		})
	}
}

func TestRender_GenericWebhookExtraEnv(t *testing.T) {
	tests := []struct {
		name     string
		extraEnv []interface{}
		want     []string
		notWant  []string
	}{
		{
			name: "extraEnv injects secret key refs",
			extraEnv: []interface{}{
				map[string]interface{}{
					"name":       "LINEAR_COMMENT_WEBHOOK_SECRET",
					"secretName": "linear-comment-webhook-secret",
					"key":        "WEBHOOK_SECRET",
				},
			},
			want: []string{
				"LINEAR_COMMENT_WEBHOOK_SECRET",
				"linear-comment-webhook-secret",
				"key: WEBHOOK_SECRET",
			},
		},
		{
			name:     "empty extraEnv omits env block",
			extraEnv: []interface{}{},
			notWant:  []string{"LINEAR_COMMENT_WEBHOOK_SECRET"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := map[string]interface{}{
				"webhookServer": map[string]interface{}{
					"sources": map[string]interface{}{
						"generic": map[string]interface{}{
							"enabled":  true,
							"replicas": 1,
							"extraEnv": tt.extraEnv,
						},
					},
				},
			}
			data, err := Render(manifests.ChartFS, vals)
			if err != nil {
				t.Fatalf("rendering chart: %v", err)
			}
			output := string(data)
			for _, s := range tt.want {
				if !strings.Contains(output, s) {
					t.Errorf("expected rendered output to contain %q", s)
				}
			}
			for _, s := range tt.notWant {
				if strings.Contains(output, s) {
					t.Errorf("did not expect rendered output to contain %q", s)
				}
			}
		})
	}
}

func TestRender_WebhookServiceType(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		serviceType string
	}{
		{
			name:        "github service type LoadBalancer",
			source:      "github",
			serviceType: "LoadBalancer",
		},
		{
			name:        "linear service type NodePort",
			source:      "linear",
			serviceType: "NodePort",
		},
		{
			name:        "generic service type LoadBalancer",
			source:      "generic",
			serviceType: "LoadBalancer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := map[string]interface{}{
				"webhookServer": map[string]interface{}{
					"sources": map[string]interface{}{
						tt.source: map[string]interface{}{
							"enabled":    true,
							"replicas":   1,
							"secretName": tt.source + "-webhook-secret",
							"service": map[string]interface{}{
								"type": tt.serviceType,
							},
						},
					},
				},
			}
			data, err := Render(manifests.ChartFS, vals)
			if err != nil {
				t.Fatalf("rendering chart: %v", err)
			}
			output := string(data)
			expected := "type: " + tt.serviceType
			if !strings.Contains(output, expected) {
				t.Errorf("expected rendered output to contain %q", expected)
			}
		})
	}
}

func TestRender_WebhookServiceTypeDefault(t *testing.T) {
	vals := map[string]interface{}{
		"webhookServer": map[string]interface{}{
			"sources": map[string]interface{}{
				"github": map[string]interface{}{
					"enabled":    true,
					"replicas":   1,
					"secretName": "github-webhook-secret",
				},
			},
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "type: ClusterIP") {
		t.Error("expected default service type to be ClusterIP")
	}
}

func TestRender_WebhookServiceMetricsPortExposure(t *testing.T) {
	tests := []struct {
		name            string
		source          string
		serviceType     string
		wantMetricsPort bool
	}{
		{
			name:            "github ClusterIP exposes metrics port",
			source:          "github",
			serviceType:     "ClusterIP",
			wantMetricsPort: true,
		},
		{
			name:            "github LoadBalancer omits metrics port",
			source:          "github",
			serviceType:     "LoadBalancer",
			wantMetricsPort: false,
		},
		{
			name:            "github NodePort omits metrics port",
			source:          "github",
			serviceType:     "NodePort",
			wantMetricsPort: false,
		},
		{
			name:            "linear LoadBalancer omits metrics port",
			source:          "linear",
			serviceType:     "LoadBalancer",
			wantMetricsPort: false,
		},
		{
			name:            "generic NodePort omits metrics port",
			source:          "generic",
			serviceType:     "NodePort",
			wantMetricsPort: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := map[string]interface{}{
				"webhookServer": map[string]interface{}{
					"sources": map[string]interface{}{
						tt.source: map[string]interface{}{
							"enabled":    true,
							"replicas":   1,
							"secretName": tt.source + "-webhook-secret",
							"service": map[string]interface{}{
								"type": tt.serviceType,
							},
						},
					},
				},
			}
			data, err := Render(manifests.ChartFS, vals)
			if err != nil {
				t.Fatalf("rendering chart: %v", err)
			}
			output := string(data)

			serviceName := "kelos-webhook-" + tt.source
			serviceSpec := extractServiceSpec(t, output, serviceName)
			hasMetricsPort := strings.Contains(serviceSpec, "name: metrics")
			if tt.wantMetricsPort && !hasMetricsPort {
				t.Errorf("expected metrics port in %s Service spec, got:\n%s", serviceName, serviceSpec)
			}
			if !tt.wantMetricsPort && hasMetricsPort {
				t.Errorf("expected no metrics port in %s Service spec when type=%s, got:\n%s", serviceName, tt.serviceType, serviceSpec)
			}
			if !strings.Contains(serviceSpec, "name: webhook") {
				t.Errorf("expected webhook port to remain in %s Service spec, got:\n%s", serviceName, serviceSpec)
			}
		})
	}
}

// extractServiceSpec returns the YAML body for the Service named name from the
// rendered chart output, or fails the test if not found.
func extractServiceSpec(t *testing.T, output, name string) string {
	t.Helper()
	docs := strings.Split(output, "---\n")
	marker := "name: " + name + "\n"
	for _, doc := range docs {
		if !strings.Contains(doc, "kind: Service") {
			continue
		}
		if !strings.Contains(doc, marker) {
			continue
		}
		return doc
	}
	t.Fatalf("Service %q not found in rendered output", name)
	return ""
}

func TestRender_WebhookServiceTypeRejectsUnsupported(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		serviceType string
	}{
		{
			name:        "github ExternalName rejected",
			source:      "github",
			serviceType: "ExternalName",
		},
		{
			name:        "linear bogus type rejected",
			source:      "linear",
			serviceType: "Bogus",
		},
		{
			name:        "generic empty type rejected",
			source:      "generic",
			serviceType: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := map[string]interface{}{
				"webhookServer": map[string]interface{}{
					"sources": map[string]interface{}{
						tt.source: map[string]interface{}{
							"enabled":    true,
							"replicas":   1,
							"secretName": tt.source + "-webhook-secret",
							"service": map[string]interface{}{
								"type": tt.serviceType,
							},
						},
					},
				},
			}
			if _, err := Render(manifests.ChartFS, vals); err == nil {
				t.Fatal("expected error rendering chart with unsupported service type")
			} else if !strings.Contains(err.Error(), "is not supported") {
				t.Errorf("expected validation error, got: %v", err)
			}
		})
	}
}

func TestRender_ParseableOutput(t *testing.T) {
	vals := map[string]interface{}{
		"image": map[string]interface{}{
			"tag": "v0.0.0-test",
		},
	}
	data, err := Render(manifests.ChartFS, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	// Verify each non-empty YAML document is actually parseable. Use the
	// Kubernetes YAML reader rather than splitting on "---\n", since the
	// rendered chart contains literal text like "rw-rw----" inside CRD
	// descriptions that would falsely match a naive separator search.
	reader := yamlutil.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	validDocs := 0
	for {
		doc, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading YAML document: %v", err)
		}
		trimmed := bytes.TrimSpace(doc)
		if len(trimmed) == 0 {
			continue
		}
		var obj map[string]interface{}
		if err := sigyaml.Unmarshal(trimmed, &obj); err != nil {
			t.Errorf("invalid YAML document: %v\n---\n%s", err, trimmed)
		}
		validDocs++
	}
	if validDocs == 0 {
		t.Fatal("expected at least one valid YAML document in rendered output")
	}
}
