package v1alpha2

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// TaskPipelinePhase represents the lifecycle of a TaskPipeline.
type TaskPipelinePhase string

const (
	// TaskPipelinePhasePending means the pipeline has been accepted but no stages have started.
	TaskPipelinePhasePending TaskPipelinePhase = "Pending"
	// TaskPipelinePhaseRunning means at least one stage is running or waiting.
	TaskPipelinePhaseRunning TaskPipelinePhase = "Running"
	// TaskPipelinePhasePaused means the pipeline is suspended by the user.
	TaskPipelinePhasePaused TaskPipelinePhase = "Paused"
	// TaskPipelinePhaseSucceeded means all stages completed successfully or were skipped.
	TaskPipelinePhaseSucceeded TaskPipelinePhase = "Succeeded"
	// TaskPipelinePhaseFailed means one or more stages failed and the pipeline cannot proceed.
	TaskPipelinePhaseFailed TaskPipelinePhase = "Failed"
)

// StagePhase represents the lifecycle of a single pipeline stage.
type StagePhase string

const (
	// StagePhasePending means the stage is waiting for upstream dependencies.
	StagePhasePending StagePhase = "Pending"
	// StagePhaseWaiting means the stage's gate has not yet cleared (no compute running).
	StagePhaseWaiting StagePhase = "Waiting"
	// StagePhaseSkipped means the when-predicate evaluated to false.
	StagePhaseSkipped StagePhase = "Skipped"
	// StagePhaseRunning means child Tasks have been created and are executing.
	StagePhaseRunning StagePhase = "Running"
	// StagePhaseSucceeded means all child Tasks completed successfully.
	StagePhaseSucceeded StagePhase = "Succeeded"
	// StagePhaseFailed means one or more child Tasks failed.
	StagePhaseFailed StagePhase = "Failed"
)

// PipelineStage defines one step in the pipeline DAG.
type PipelineStage struct {
	// Name identifies this stage. Must be unique within the pipeline.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Name string `json:"name"`

	// DependsOn lists stage names that must reach a terminal phase (Succeeded
	// or Skipped) before this stage becomes eligible to run.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// When is a CEL expression evaluated against prior stage results and phases.
	// If the expression evaluates to false, the stage is marked Skipped without
	// creating any Tasks. Skipped stages count as resolved for downstream dependsOn.
	//
	// Available variables:
	//   stages - map[string]StageContext where StageContext has fields:
	//     results: map[string]string (merged task results)
	//     phase: string (Succeeded, Failed, Skipped)
	//
	// Examples:
	//   stages.build.phase == "Succeeded"
	//   int(stages.lint.results.errorCount) == 0
	//   stages.test.results.status == "pass"
	//
	// +optional
	When string `json:"when,omitempty"`

	// WaitFor defines an external gate that must clear before child Tasks are
	// created. The pipeline controller handles the wait without running any Pods.
	// +optional
	WaitFor *Gate `json:"waitFor,omitempty"`

	// Tasks defines the task templates to run in this stage. All tasks within
	// a stage execute in parallel. When matrix is set, each task template is
	// expanded across all parameter combinations.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Tasks []PipelineTaskTemplate `json:"tasks"`

	// Matrix defines parameter axes for fan-out. Each combination of parameter
	// values produces one task per task template. When empty, each task template
	// produces exactly one Task.
	// +optional
	Matrix *MatrixSpec `json:"matrix,omitempty"`
}

// PipelineTaskTemplate defines the template for a Task created within a pipeline stage.
type PipelineTaskTemplate struct {
	// Name is a short identifier for this task within the stage. Combined with
	// the pipeline name and stage name to form the child Task's metadata.name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Name string `json:"name"`

	// Worker defines the execution environment for this task.
	// +optional
	Worker *WorkerSpec `json:"worker,omitempty"`

	// WorkerPoolRef references a WorkerPool for persistent execution.
	// Mutually exclusive with worker.
	// +optional
	WorkerPoolRef *WorkerPoolReference `json:"workerPoolRef,omitempty"`

	// PromptTemplate is the Go template for the task prompt. It has access to:
	//   {{.Stages}} - map of stage name to {Results: map[string]string, Phase: string}
	//   {{.Matrix}} - map of parameter name to value (when matrix is used)
	//   {{index .Stages "stage-name" "Results" "key"}}
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	PromptTemplate string `json:"promptTemplate"`

	// Branch specifies the git branch for the task. Supports Go template
	// interpolation with the same context as promptTemplate.
	// +optional
	Branch string `json:"branch,omitempty"`

	// TTLSecondsAfterFinished auto-deletes individual child Tasks after completion.
	// When unset, child Tasks are deleted when the parent pipeline is deleted
	// (via owner reference garbage collection).
	// +optional
	// +kubebuilder:validation:Minimum=0
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`

	// PodOverrides allows customizing the agent pod configuration for this task.
	// +optional
	PodOverrides *PodOverrides `json:"podOverrides,omitempty"`
}

// MatrixSpec defines parameter axes for fan-out within a stage.
type MatrixSpec struct {
	// Params maps parameter names to lists of values. Each combination of
	// values across all parameters produces one Task per task template.
	//
	// Example: {repo: [auth, billing], env: [staging, prod]} produces 4 tasks.
	//
	// Values are available in templates as {{.Matrix.paramName}}.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinProperties=1
	Params map[string][]string `json:"params"`
}

// Gate defines what the pipeline waits for before a stage proceeds.
// The wait consumes zero compute (no Pods). Exactly one gate type must be set.
//
// +kubebuilder:validation:XValidation:rule="[has(self.webhook), has(self.githubCheck), has(self.approval), has(self.duration)].filter(x, x).size() == 1",message="exactly one gate type must be set"
type Gate struct {
	// Webhook waits for an incoming webhook delivery matching the source and
	// filters. The kelos-webhook-server clears the gate by patching pipeline status.
	// +optional
	Webhook *WebhookGate `json:"webhook,omitempty"`

	// GitHubCheck waits for a GitHub check run to reach a target conclusion.
	// The pipeline controller polls the GitHub Checks API at the configured interval.
	// +optional
	GitHubCheck *GitHubCheckGate `json:"githubCheck,omitempty"`

	// Approval waits for explicit human approval via a status patch.
	// +optional
	Approval *ApprovalGate `json:"approval,omitempty"`

	// Duration waits for a fixed amount of time before proceeding.
	// +optional
	Duration *metav1.Duration `json:"duration,omitempty"`
}

// WebhookGate pauses a stage until an incoming webhook clears it.
type WebhookGate struct {
	// Source identifies the webhook source. This matches the path segment in
	// the webhook URL (/webhook/<source>) for generic webhooks, or "github"
	// / "linear" for those sources.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Source string `json:"source"`

	// Filters are conditions on the webhook payload that must all match for
	// the gate to clear. When empty, any delivery from this source clears it.
	// Uses the same GenericWebhookFilter type as TaskSpawner.
	// +optional
	Filters []GenericWebhookFilter `json:"filters,omitempty"`
}

// GitHubCheckGate pauses a stage until a GitHub check run reaches a target state.
type GitHubCheckGate struct {
	// Owner is the GitHub repository owner (user or organization).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Owner string `json:"owner"`

	// Repo is the GitHub repository name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// Ref is the git ref (commit SHA or branch name) whose check runs are polled.
	// Supports Go template interpolation: {{index .Stages "build" "Results" "commit"}}
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Ref string `json:"ref"`

	// CheckName is the name of the check run to wait for.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	CheckName string `json:"checkName"`

	// TargetConclusion is the check run conclusion that clears the gate.
	// +kubebuilder:validation:Enum=success;neutral;skipped
	// +kubebuilder:default=success
	// +optional
	TargetConclusion string `json:"targetConclusion,omitempty"`

	// PollInterval is how often the controller checks the GitHub API.
	// Defaults to 30s. Minimum 10s to avoid rate limiting.
	// +optional
	PollInterval *metav1.Duration `json:"pollInterval,omitempty"`
}

// ApprovalGate pauses a stage until a human explicitly clears it.
type ApprovalGate struct {
	// Approvers lists identifiers (usernames, team names) allowed to clear
	// this gate. When empty, any authenticated user can approve.
	// +optional
	Approvers []string `json:"approvers,omitempty"`

	// Message is a human-readable description shown to users waiting to approve.
	// +optional
	Message string `json:"message,omitempty"`
}

// TaskPipelineSpec defines the desired state of a TaskPipeline.
//
// +kubebuilder:validation:XValidation:rule="self.stages.all(s, s.dependsOn.all(d, self.stages.exists(t, t.name == d)))",message="dependsOn references a stage name that does not exist in this pipeline"
type TaskPipelineSpec struct {
	// Stages defines the pipeline stages. Stages form a DAG via dependsOn.
	// Stages without dependsOn run immediately (or after their gate clears).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Stages []PipelineStage `json:"stages"`

	// Timeout is the maximum wall-clock duration for the entire pipeline.
	// If exceeded, all running Tasks are terminated and the pipeline fails.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// TTLSecondsAfterFinished specifies how long the pipeline (and its child
	// Tasks) persist after reaching a terminal phase. Deletion cascades to
	// all child Tasks via owner references.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`

	// Suspend, when true, prevents new stages from starting. Running Tasks
	// continue to completion but no new Tasks are created. Set to false to resume.
	// +optional
	// +kubebuilder:default=false
	Suspend *bool `json:"suspend,omitempty"`
}

// StageStatus tracks the observed state of a pipeline stage.
type StageStatus struct {
	// Name corresponds to PipelineStage.Name.
	Name string `json:"name"`

	// Phase is the current lifecycle phase of this stage.
	Phase StagePhase `json:"phase"`

	// TaskNames lists the Kubernetes names of child Tasks created for this stage.
	// +optional
	TaskNames []string `json:"taskNames,omitempty"`

	// Results is the merged result map from all child Tasks in this stage.
	// For matrix stages, results are keyed as "taskName.key" to avoid collisions.
	// +optional
	Results map[string]string `json:"results,omitempty"`

	// GateStatus tracks the state of the stage's waitFor gate.
	// +optional
	GateStatus *GateStatus `json:"gateStatus,omitempty"`

	// StartTime is when the stage transitioned out of Pending.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the stage reached a terminal phase.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message provides human-readable detail about the current state.
	// +optional
	Message string `json:"message,omitempty"`
}

// GateStatus tracks whether an external gate has been cleared.
type GateStatus struct {
	// Cleared indicates the gate condition has been satisfied.
	Cleared bool `json:"cleared"`

	// ClearedAt is when the gate was cleared.
	// +optional
	ClearedAt *metav1.Time `json:"clearedAt,omitempty"`

	// ClearedBy identifies what cleared the gate (delivery ID, approver, "timer", etc).
	// +optional
	ClearedBy string `json:"clearedBy,omitempty"`

	// LastPollTime is when the controller last checked a polled gate (githubCheck).
	// +optional
	LastPollTime *metav1.Time `json:"lastPollTime,omitempty"`
}

// TaskPipelineStatus defines the observed state of TaskPipeline.
type TaskPipelineStatus struct {
	// Phase is the overall pipeline lifecycle phase.
	// +optional
	Phase TaskPipelinePhase `json:"phase,omitempty"`

	// Stages tracks per-stage observed state.
	// +optional
	Stages []StageStatus `json:"stages,omitempty"`

	// StartTime is when the pipeline began executing (first stage left Pending).
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the pipeline reached a terminal phase.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Conditions provides Kubernetes-standard condition tracking.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TaskPipeline orchestrates a DAG of stages, each containing parallel Tasks,
// with support for conditional execution, external gates, and matrix fan-out.
type TaskPipeline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TaskPipelineSpec   `json:"spec,omitempty"`
	Status TaskPipelineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TaskPipelineList contains a list of TaskPipeline.
type TaskPipelineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TaskPipeline `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TaskPipeline{}, &TaskPipelineList{})
}
