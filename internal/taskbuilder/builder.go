package taskbuilder

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"text/template"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/source"
)

// TaskBuilder creates Task resources from TaskSpawner templates.
// It handles template rendering, metadata generation, and annotation management
// for both traditional sources (GitHub Issues, Cron) and webhook sources.
type TaskBuilder struct {
	spawner *kelosv1alpha1.TaskSpawner
}

// NewTaskBuilder creates a new TaskBuilder for the given TaskSpawner.
func NewTaskBuilder(spawner *kelosv1alpha1.TaskSpawner) *TaskBuilder {
	return &TaskBuilder{spawner: spawner}
}

// TaskInput represents the data used to create a Task. It can come from
// traditional sources (WorkItem) or webhook events (map[string]interface{}).
type TaskInput interface {
	// GetID returns a unique identifier for this work item
	GetID() string
	// GetTemplateData returns the data structure used for template rendering
	GetTemplateData() interface{}
	// GetSourceAnnotations returns source-specific annotations to add to the Task
	GetSourceAnnotations() map[string]string
}

// WorkItemInput adapts the existing source.WorkItem to the TaskInput interface.
type WorkItemInput struct {
	Item source.WorkItem
}

func (w WorkItemInput) GetID() string {
	return w.Item.ID
}

func (w WorkItemInput) GetTemplateData() interface{} {
	// Convert WorkItem to the template data format expected by source.RenderTemplate
	kind := w.Item.Kind
	if kind == "" {
		kind = "Issue"
	}

	return struct {
		ID             string
		Number         int
		Title          string
		Body           string
		URL            string
		Labels         string
		Comments       string
		Kind           string
		Branch         string
		ReviewState    string
		ReviewComments string
		Time           string
		Schedule       string
	}{
		ID:             w.Item.ID,
		Number:         w.Item.Number,
		Title:          w.Item.Title,
		Body:           w.Item.Body,
		URL:            w.Item.URL,
		Labels:         strings.Join(w.Item.Labels, ", "),
		Comments:       w.Item.Comments,
		Kind:           kind,
		Branch:         w.Item.Branch,
		ReviewState:    w.Item.ReviewState,
		ReviewComments: w.Item.ReviewComments,
		Time:           w.Item.Time,
		Schedule:       w.Item.Schedule,
	}
}

func (w WorkItemInput) GetSourceAnnotations() map[string]string {
	return getSourceAnnotations(w.Item, w.Item.Kind)
}

// WebhookInput adapts webhook template data to the TaskInput interface.
type WebhookInput struct {
	ID           string
	TemplateVars map[string]interface{}
	DeliveryID   string // For idempotency (e.g., X-GitHub-Delivery header)
	SourceType   string // "github", "linear"
	EventType    string // "issue_comment", "push", etc.
}

func (wh WebhookInput) GetID() string {
	return wh.ID
}

func (wh WebhookInput) GetTemplateData() interface{} {
	return wh.TemplateVars
}

func (wh WebhookInput) GetSourceAnnotations() map[string]string {
	annotations := make(map[string]string)

	// Add webhook-specific annotations for idempotency and auditing
	annotations["kelos.dev/webhook-source"] = wh.SourceType
	annotations["kelos.dev/webhook-event"] = wh.EventType
	if wh.DeliveryID != "" {
		annotations["kelos.dev/webhook-delivery"] = wh.DeliveryID
	}

	// Add GitHub reporting annotations if this is a GitHub webhook
	// with issue/PR data for backward compatibility with existing reporting
	if wh.SourceType == "github" {
		if kind, ok := wh.TemplateVars["Kind"].(string); ok {
			if kind == "Issue" || kind == "PR" {
				sourceKind := "issue"
				if kind == "PR" {
					sourceKind = "pull-request"
				}
				annotations[reporting.AnnotationSourceKind] = sourceKind

				if number, ok := wh.TemplateVars["Number"].(int); ok && number > 0 {
					annotations[reporting.AnnotationSourceNumber] = strconv.Itoa(number)
				}

				// Note: GitHub reporting enabled flag would need to be passed separately
				// or determined from the TaskSpawner configuration
			}
		}
	}

	return annotations
}

// BuildTask creates a Task from the TaskSpawner template and input data.
func (tb *TaskBuilder) BuildTask(input TaskInput) (*kelosv1alpha1.Task, error) {
	// Generate task name
	taskName := fmt.Sprintf("%s-%s", tb.spawner.Name, input.GetID())

	// Render prompt template
	prompt, err := tb.renderTemplate(tb.spawner.Spec.TaskTemplate.PromptTemplate, input.GetTemplateData())
	if err != nil {
		return nil, fmt.Errorf("rendering prompt template: %w", err)
	}

	// Render task metadata (labels and annotations from TaskTemplate.Metadata)
	renderedLabels, renderedAnnotations, err := tb.renderTaskTemplateMetadata(input.GetTemplateData())
	if err != nil {
		return nil, fmt.Errorf("rendering task template metadata: %w", err)
	}

	// Build labels (user labels + required kelos.dev/taskspawner label)
	labels := make(map[string]string)
	for k, v := range renderedLabels {
		labels[k] = v
	}
	labels["kelos.dev/taskspawner"] = tb.spawner.Name

	// Build annotations (user annotations + source annotations)
	annotations := mergeStringMaps(renderedAnnotations, input.GetSourceAnnotations())

	// Create Task spec
	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:        taskName,
			Namespace:   tb.spawner.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:                    tb.spawner.Spec.TaskTemplate.Type,
			Prompt:                  prompt,
			Credentials:             tb.spawner.Spec.TaskTemplate.Credentials,
			Model:                   tb.spawner.Spec.TaskTemplate.Model,
			Image:                   tb.spawner.Spec.TaskTemplate.Image,
			TTLSecondsAfterFinished: tb.spawner.Spec.TaskTemplate.TTLSecondsAfterFinished,
			PodOverrides:            tb.spawner.Spec.TaskTemplate.PodOverrides,
		},
	}

	// Set optional fields
	if tb.spawner.Spec.TaskTemplate.WorkspaceRef != nil {
		task.Spec.WorkspaceRef = tb.spawner.Spec.TaskTemplate.WorkspaceRef
	}
	if tb.spawner.Spec.TaskTemplate.AgentConfigRef != nil {
		task.Spec.AgentConfigRef = tb.spawner.Spec.TaskTemplate.AgentConfigRef
	}
	if len(tb.spawner.Spec.TaskTemplate.DependsOn) > 0 {
		task.Spec.DependsOn = tb.spawner.Spec.TaskTemplate.DependsOn
	}

	// Render branch template if configured
	if tb.spawner.Spec.TaskTemplate.Branch != "" {
		branch, err := tb.renderTemplate(tb.spawner.Spec.TaskTemplate.Branch, input.GetTemplateData())
		if err != nil {
			return nil, fmt.Errorf("rendering branch template: %w", err)
		}
		task.Spec.Branch = branch
	}

	// Set upstream repo if configured
	if tb.spawner.Spec.TaskTemplate.UpstreamRepo != "" {
		task.Spec.UpstreamRepo = tb.spawner.Spec.TaskTemplate.UpstreamRepo
	}

	return task, nil
}

// renderTemplate renders a Go text/template string with the given data.
func (tb *TaskBuilder) renderTemplate(tmplStr string, data interface{}) (string, error) {
	if tmplStr == "" {
		return "", nil
	}

	tmpl, err := template.New("tmpl").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}

	return buf.String(), nil
}

// renderTaskTemplateMetadata renders taskTemplate.metadata label and annotation
// values using the template data.
func (tb *TaskBuilder) renderTaskTemplateMetadata(data interface{}) (labels map[string]string, annotations map[string]string, err error) {
	meta := tb.spawner.Spec.TaskTemplate.Metadata
	if meta == nil {
		return nil, nil, nil
	}

	if len(meta.Labels) > 0 {
		labels = make(map[string]string)
		for k, v := range meta.Labels {
			rendered, err := tb.renderTemplate(v, data)
			if err != nil {
				return nil, nil, fmt.Errorf("label %q: %w", k, err)
			}
			labels[k] = rendered
		}
	}

	if len(meta.Annotations) > 0 {
		annotations = make(map[string]string)
		for k, v := range meta.Annotations {
			rendered, err := tb.renderTemplate(v, data)
			if err != nil {
				return nil, nil, fmt.Errorf("annotation %q: %w", k, err)
			}
			annotations[k] = rendered
		}
	}

	return labels, annotations, nil
}

// mergeStringMaps returns a new map with keys from base, then keys from overlay
// overwriting on duplicate keys.
func mergeStringMaps(base, overlay map[string]string) map[string]string {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	out := make(map[string]string)
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

// getSourceAnnotations returns annotations that stamp source metadata
// onto a spawned Task for GitHub Issues/PRs. These annotations enable
// downstream consumers (such as the reporting watcher) to identify the
// originating issue or PR.
func getSourceAnnotations(item source.WorkItem, kind string) map[string]string {
	// Only add GitHub source annotations for Issue/PR types
	if kind != "Issue" && kind != "PR" {
		return nil
	}

	sourceKind := "issue"
	if kind == "PR" {
		sourceKind = "pull-request"
	}

	annotations := map[string]string{
		reporting.AnnotationSourceKind:   sourceKind,
		reporting.AnnotationSourceNumber: strconv.Itoa(item.Number),
	}

	return annotations
}

// BuildTaskFromWorkItem creates a Task from a WorkItem (for existing sources).
// This is a convenience method that wraps BuildTask with WorkItemInput.
func (tb *TaskBuilder) BuildTaskFromWorkItem(item source.WorkItem) (*kelosv1alpha1.Task, error) {
	input := WorkItemInput{Item: item}
	return tb.BuildTask(input)
}

// BuildTaskFromWebhook creates a Task from webhook template data.
// This is a convenience method that wraps BuildTask with WebhookInput.
func (tb *TaskBuilder) BuildTaskFromWebhook(id string, templateVars map[string]interface{}, deliveryID, sourceType, eventType string) (*kelosv1alpha1.Task, error) {
	input := WebhookInput{
		ID:           id,
		TemplateVars: templateVars,
		DeliveryID:   deliveryID,
		SourceType:   sourceType,
		EventType:    eventType,
	}
	return tb.BuildTask(input)
}
