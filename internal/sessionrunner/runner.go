/*
Copyright 2025 Kelos contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sessionrunner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	kelosversioned "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned"
)

const (
	annotationAssignedTask   = "kelos.dev/assigned-task"
	annotationTaskStatus     = "kelos.dev/task-status"
	annotationTasksCompleted = "kelos.dev/tasks-completed"
	annotationSessionStart   = "kelos.dev/session-start-time"

	defaultIdleTimeout        = 30 * time.Minute
	defaultMaxSessionDuration = 8 * time.Hour
	pollInterval              = 3 * time.Second
)

// Config holds the session runner configuration, typically from environment variables.
type Config struct {
	PodName            string
	PodNamespace       string
	AgentType          string
	TaskSpawner        string
	IdleTimeout        time.Duration
	MaxTasksPerSession int32
	MaxSessionDuration time.Duration
}

// ConfigFromEnv reads session runner configuration from environment variables.
func ConfigFromEnv() Config {
	cfg := Config{
		PodName:            os.Getenv("KELOS_POD_NAME"),
		PodNamespace:       os.Getenv("KELOS_POD_NAMESPACE"),
		AgentType:          os.Getenv("KELOS_AGENT_TYPE"),
		TaskSpawner:        os.Getenv("KELOS_TASKSPAWNER"),
		IdleTimeout:        defaultIdleTimeout,
		MaxSessionDuration: defaultMaxSessionDuration,
	}

	if v := os.Getenv("KELOS_IDLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.IdleTimeout = d
		}
	}
	if v := os.Getenv("KELOS_MAX_TASKS_PER_SESSION"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			cfg.MaxTasksPerSession = int32(n)
		}
	}
	if v := os.Getenv("KELOS_MAX_SESSION_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.MaxSessionDuration = d
		}
	}

	return cfg
}

// Runner implements the session runner main loop.
type Runner struct {
	config      Config
	kubeClient  kubernetes.Interface
	kelosClient kelosversioned.Interface
	workspace   *WorkspaceManager
}

// NewRunner creates a new session runner.
func NewRunner(cfg Config) (*Runner, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	kubeClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	kelosClient, err := kelosversioned.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kelos client: %w", err)
	}

	return &Runner{
		config:      cfg,
		kubeClient:  kubeClient,
		kelosClient: kelosClient,
		workspace:   NewWorkspaceManager(),
	}, nil
}

// Run executes the session runner main loop. It blocks until the session
// ends (idle timeout, max tasks, max duration, or context cancellation).
func (r *Runner) Run(ctx context.Context) error {
	fmt.Printf("Session runner starting: pod=%s namespace=%s spawner=%s\n",
		r.config.PodName, r.config.PodNamespace, r.config.TaskSpawner)

	sessionStart := time.Now()
	tasksCompleted := int32(0)
	lastTaskTime := time.Now()
	lastProcessedTask := ""

	for {
		select {
		case <-ctx.Done():
			fmt.Println("Session runner shutting down: context cancelled")
			return nil
		default:
		}

		// Check session limits.
		if r.config.MaxSessionDuration > 0 && time.Since(sessionStart) > r.config.MaxSessionDuration {
			fmt.Println("Session runner exiting: max session duration reached")
			return nil
		}
		if r.config.MaxTasksPerSession > 0 && tasksCompleted >= r.config.MaxTasksPerSession {
			fmt.Println("Session runner exiting: max tasks per session reached")
			return nil
		}
		if time.Since(lastTaskTime) > r.config.IdleTimeout {
			fmt.Println("Session runner exiting: idle timeout reached")
			return nil
		}

		// Check for task assignment.
		taskName, err := r.getAssignedTask(ctx)
		if err != nil {
			fmt.Printf("Error checking task assignment: %v\n", err)
			time.Sleep(pollInterval)
			continue
		}

		if taskName == "" || taskName == lastProcessedTask {
			time.Sleep(pollInterval)
			continue
		}

		// Process the assigned task.
		fmt.Printf("Task assigned: %s\n", taskName)

		if err := r.processTask(ctx, taskName); err != nil {
			fmt.Printf("Task %s failed: %v\n", taskName, err)
			if setErr := r.setTaskStatus(ctx, "failed"); setErr != nil {
				fmt.Printf("Error setting task status to failed: %v\n", setErr)
			}
		} else {
			fmt.Printf("Task %s completed successfully\n", taskName)
			if setErr := r.setTaskStatus(ctx, "succeeded"); setErr != nil {
				fmt.Printf("Error setting task status to succeeded: %v\n", setErr)
			}
		}

		lastProcessedTask = taskName
		lastTaskTime = time.Now()
		tasksCompleted++
		if setErr := r.setAnnotation(ctx, annotationTasksCompleted, strconv.Itoa(int(tasksCompleted))); setErr != nil {
			fmt.Printf("Error updating tasks completed count: %v\n", setErr)
		}
	}
}

// getAssignedTask checks the pod's annotations for a task assignment.
func (r *Runner) getAssignedTask(ctx context.Context) (string, error) {
	pod, err := r.kubeClient.CoreV1().Pods(r.config.PodNamespace).Get(ctx, r.config.PodName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return pod.Annotations[annotationAssignedTask], nil
}

// processTask handles a single task: workspace reset, agent invocation.
func (r *Runner) processTask(ctx context.Context, taskName string) error {
	// Read Task object for prompt, branch, etc.
	task, err := r.kelosClient.ApiV1alpha1().Tasks(r.config.PodNamespace).Get(ctx, taskName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get task %s: %w", taskName, err)
	}

	// Set status to running.
	if err := r.setTaskStatus(ctx, "running"); err != nil {
		return fmt.Errorf("failed to set task status to running: %w", err)
	}

	// Reset workspace.
	if err := r.workspace.Reset(ctx, task.Spec.Branch); err != nil {
		return fmt.Errorf("workspace reset failed: %w", err)
	}

	// Invoke the agent entrypoint and capture outputs.
	agentOutput, agentErr := r.runAgent(ctx, task)

	// Parse and persist outputs to Task status regardless of success/failure.
	if outputs := parseOutputs(agentOutput); len(outputs) > 0 {
		results := resultsFromOutputs(outputs)
		if err := r.updateTaskOutputs(ctx, taskName, outputs, results); err != nil {
			fmt.Printf("Error updating task outputs: %v\n", err)
		}
	}

	return agentErr
}

// runAgent invokes the agent entrypoint with the task prompt.
// It returns the captured stdout content and any execution error.
func (r *Runner) runAgent(ctx context.Context, task *kelosv1alpha1.Task) (string, error) {
	entrypoint := "/kelos_entrypoint.sh"

	// Set branch env var if present.
	env := os.Environ()
	if task.Spec.Branch != "" {
		env = append(env, fmt.Sprintf("KELOS_BRANCH=%s", task.Spec.Branch))
	}

	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, entrypoint, task.Spec.Prompt)
	cmd.Dir = "/workspace/repo"
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = os.Stderr
	cmd.Env = env

	err := cmd.Run()
	return buf.String(), err
}

// updateTaskOutputs writes captured outputs and results to the Task status.
func (r *Runner) updateTaskOutputs(ctx context.Context, taskName string, outputs []string, results map[string]string) error {
	task, err := r.kelosClient.ApiV1alpha1().Tasks(r.config.PodNamespace).Get(ctx, taskName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	task.Status.Outputs = outputs
	task.Status.Results = results
	now := metav1.Now()
	task.Status.CompletionTime = &now
	if task.Status.StartTime == nil {
		task.Status.StartTime = &now
	}
	_, err = r.kelosClient.ApiV1alpha1().Tasks(r.config.PodNamespace).UpdateStatus(ctx, task, metav1.UpdateOptions{})
	return err
}

// setTaskStatus sets the kelos.dev/task-status annotation on the pod.
func (r *Runner) setTaskStatus(ctx context.Context, status string) error {
	return r.setAnnotation(ctx, annotationTaskStatus, status)
}

// setAnnotation sets a single annotation on the pod with retry-on-conflict.
func (r *Runner) setAnnotation(ctx context.Context, key, value string) error {
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		pod, err := r.kubeClient.CoreV1().Pods(r.config.PodNamespace).Get(ctx, r.config.PodName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pod.Annotations == nil {
			pod.Annotations = make(map[string]string)
		}
		pod.Annotations[key] = value
		_, err = r.kubeClient.CoreV1().Pods(r.config.PodNamespace).Update(ctx, pod, metav1.UpdateOptions{})
		if err == nil {
			return nil
		}
		// Retry on conflict.
		if !apierrors.IsConflict(err) {
			return err
		}
	}
	return fmt.Errorf("failed to set annotation %s after %d retries", key, maxRetries)
}

const (
	outputStartMarker = "---KELOS_OUTPUTS_START---"
	outputEndMarker   = "---KELOS_OUTPUTS_END---"
)

// parseOutputs extracts output lines from log data between markers.
func parseOutputs(logData string) []string {
	startIdx := strings.Index(logData, outputStartMarker)
	if startIdx == -1 {
		return nil
	}
	endIdx := strings.Index(logData, outputEndMarker)
	if endIdx == -1 || endIdx <= startIdx {
		return nil
	}

	between := logData[startIdx+len(outputStartMarker) : endIdx]
	between = strings.TrimSpace(between)
	if between == "" {
		return nil
	}

	lines := strings.Split(between, "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// resultsFromOutputs builds a key-value map from output lines in "key: value" format.
func resultsFromOutputs(outputs []string) map[string]string {
	if len(outputs) == 0 {
		return nil
	}
	var result map[string]string
	for _, line := range outputs {
		key, value, ok := strings.Cut(line, ": ")
		if !ok || key == "" {
			continue
		}
		if result == nil {
			result = make(map[string]string)
		}
		result[key] = value
	}
	return result
}
