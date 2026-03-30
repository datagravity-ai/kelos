package webhook

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/go-github/v66/github"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// GitHubEventData contains parsed GitHub webhook event data with common fields
// extracted for easy access in filters and templates.
type GitHubEventData struct {
	Event      string                 // GitHub event type (e.g., "issue_comment", "push")
	Action     string                 // Event action (e.g., "created", "opened")
	Sender     *github.User           // User who triggered the event
	Repository *github.Repository     // Repository where the event occurred
	RawEvent   interface{}            // Full parsed event struct from go-github
	RawPayload map[string]interface{} // Raw JSON payload for template access
}

// ParseGitHubWebhook parses a GitHub webhook payload using go-github and extracts
// common fields for filtering and template rendering.
func ParseGitHubWebhook(eventType string, payload []byte) (*GitHubEventData, error) {
	// Parse using go-github for type safety and completeness
	event, err := github.ParseWebHook(eventType, payload)
	if err != nil {
		return nil, fmt.Errorf("parsing GitHub webhook: %w", err)
	}

	// Parse raw payload for template access to any field
	var rawPayload map[string]interface{}
	if err := json.Unmarshal(payload, &rawPayload); err != nil {
		return nil, fmt.Errorf("parsing raw payload: %w", err)
	}

	data := &GitHubEventData{
		Event:      eventType,
		RawEvent:   event,
		RawPayload: rawPayload,
	}

	// Extract common fields based on event type
	switch e := event.(type) {
	case *github.IssueCommentEvent:
		data.Action = getString(e.Action)
		data.Sender = e.Sender
		data.Repository = e.Repo
	case *github.IssuesEvent:
		data.Action = getString(e.Action)
		data.Sender = e.Sender
		data.Repository = e.Repo
	case *github.PullRequestEvent:
		data.Action = getString(e.Action)
		data.Sender = e.Sender
		data.Repository = e.Repo
	case *github.PullRequestReviewEvent:
		data.Action = getString(e.Action)
		data.Sender = e.Sender
		data.Repository = e.Repo
	case *github.PullRequestReviewCommentEvent:
		data.Action = getString(e.Action)
		data.Sender = e.Sender
		data.Repository = e.Repo
	case *github.PushEvent:
		data.Sender = e.Sender
		// Convert PushEventRepository to Repository for consistency
		if e.Repo != nil {
			data.Repository = &github.Repository{
				Name:     e.Repo.Name,
				FullName: e.Repo.FullName,
				HTMLURL:  e.Repo.HTMLURL,
			}
		}
		// Push events don't have an "action" field
	default:
		// Try to extract sender and repo from raw payload for unknown event types
		if sender, ok := rawPayload["sender"].(map[string]interface{}); ok {
			if login, ok := sender["login"].(string); ok {
				data.Sender = &github.User{Login: &login}
			}
		}
		if repo, ok := rawPayload["repository"].(map[string]interface{}); ok {
			if name, ok := repo["name"].(string); ok {
				data.Repository = &github.Repository{Name: &name}
			}
		}
		if action, ok := rawPayload["action"].(string); ok {
			data.Action = action
		}
	}

	return data, nil
}

// MatchesGitHubEvent checks whether a GitHub webhook event matches a TaskSpawner's
// GitHubWebhook configuration. Returns true if the event should trigger a task.
func MatchesGitHubEvent(cfg *kelosv1alpha1.GitHubWebhook, eventType string, payload []byte) bool {
	// Check if the event type is in the configured events list
	eventMatched := false
	for _, e := range cfg.Events {
		if e == eventType {
			eventMatched = true
			break
		}
	}
	if !eventMatched {
		return false
	}

	// Parse the webhook using go-github
	data, err := ParseGitHubWebhook(eventType, payload)
	if err != nil {
		return false
	}

	// If no filters are configured, all matching event types trigger
	if len(cfg.Filters) == 0 {
		return true
	}

	// OR semantics: any matching filter triggers
	for _, f := range cfg.Filters {
		matches, err := MatchesGitHubFilter(f, data)
		if err != nil {
			return false
		}
		if matches {
			return true
		}
	}

	return false
}

// MatchesGitHubFilter evaluates whether a GitHub webhook event matches the given filter.
func MatchesGitHubFilter(filter kelosv1alpha1.GitHubWebhookFilter, data *GitHubEventData) (bool, error) {
	// Event type must match (required field)
	if filter.Event != data.Event {
		return false, nil
	}

	// Check action filter
	if filter.Action != "" && filter.Action != data.Action {
		return false, nil
	}

	// Check author filter
	if filter.Author != "" {
		if data.Sender == nil || !strings.EqualFold(getString(data.Sender.Login), filter.Author) {
			return false, nil
		}
	}

	// Event-specific filtering
	switch e := data.RawEvent.(type) {
	case *github.IssueCommentEvent:
		return matchesIssueCommentEvent(filter, e)
	case *github.IssuesEvent:
		return matchesIssuesEvent(filter, e)
	case *github.PullRequestEvent:
		return matchesPullRequestEvent(filter, e)
	case *github.PullRequestReviewEvent:
		return matchesPullRequestReviewEvent(filter, e)
	case *github.PullRequestReviewCommentEvent:
		return matchesPullRequestReviewCommentEvent(filter, e)
	case *github.PushEvent:
		return matchesPushEvent(filter, e)
	default:
		// For unknown event types, only basic filters apply (event, action, author)
		return true, nil
	}
}

func matchesIssueCommentEvent(filter kelosv1alpha1.GitHubWebhookFilter, event *github.IssueCommentEvent) (bool, error) {
	// Body contains filter
	if filter.BodyContains != "" {
		if event.Comment == nil || !strings.Contains(getString(event.Comment.Body), filter.BodyContains) {
			return false, nil
		}
	}

	// Issue/PR labels filter
	if len(filter.Labels) > 0 {
		if event.Issue == nil {
			return false, nil
		}
		if !hasAllLabels(event.Issue.Labels, filter.Labels) {
			return false, nil
		}
	}

	// Issue/PR state filter
	if filter.State != "" {
		if event.Issue == nil || getString(event.Issue.State) != filter.State {
			return false, nil
		}
	}

	// Draft filter (for PRs)
	if filter.Draft != nil {
		if event.Issue == nil || event.Issue.PullRequestLinks == nil {
			// Not a PR, but draft filter specified
			return false, nil
		}
		if event.Issue.Draft != nil && *event.Issue.Draft != *filter.Draft {
			return false, nil
		}
	}

	return true, nil
}

func matchesIssuesEvent(filter kelosv1alpha1.GitHubWebhookFilter, event *github.IssuesEvent) (bool, error) {
	// Labels filter
	if len(filter.Labels) > 0 {
		if event.Issue == nil {
			return false, nil
		}
		if !hasAllLabels(event.Issue.Labels, filter.Labels) {
			return false, nil
		}
	}

	// State filter
	if filter.State != "" {
		if event.Issue == nil || getString(event.Issue.State) != filter.State {
			return false, nil
		}
	}

	return true, nil
}

func matchesPullRequestEvent(filter kelosv1alpha1.GitHubWebhookFilter, event *github.PullRequestEvent) (bool, error) {
	// Labels filter
	if len(filter.Labels) > 0 {
		if event.PullRequest == nil {
			return false, nil
		}
		if !hasAllLabels(event.PullRequest.Labels, filter.Labels) {
			return false, nil
		}
	}

	// State filter
	if filter.State != "" {
		if event.PullRequest == nil || getString(event.PullRequest.State) != filter.State {
			return false, nil
		}
	}

	// Draft filter
	if filter.Draft != nil {
		if event.PullRequest == nil {
			return false, nil
		}
		if event.PullRequest.Draft != nil && *event.PullRequest.Draft != *filter.Draft {
			return false, nil
		}
	}

	return true, nil
}

func matchesPullRequestReviewEvent(filter kelosv1alpha1.GitHubWebhookFilter, event *github.PullRequestReviewEvent) (bool, error) {
	// Body contains filter
	if filter.BodyContains != "" {
		if event.Review == nil || !strings.Contains(getString(event.Review.Body), filter.BodyContains) {
			return false, nil
		}
	}

	// PR labels filter
	if len(filter.Labels) > 0 {
		if event.PullRequest == nil {
			return false, nil
		}
		if !hasAllLabels(event.PullRequest.Labels, filter.Labels) {
			return false, nil
		}
	}

	// PR state filter
	if filter.State != "" {
		if event.PullRequest == nil || getString(event.PullRequest.State) != filter.State {
			return false, nil
		}
	}

	// Draft filter
	if filter.Draft != nil {
		if event.PullRequest == nil {
			return false, nil
		}
		if event.PullRequest.Draft != nil && *event.PullRequest.Draft != *filter.Draft {
			return false, nil
		}
	}

	return true, nil
}

func matchesPullRequestReviewCommentEvent(filter kelosv1alpha1.GitHubWebhookFilter, event *github.PullRequestReviewCommentEvent) (bool, error) {
	// Body contains filter
	if filter.BodyContains != "" {
		if event.Comment == nil || !strings.Contains(getString(event.Comment.Body), filter.BodyContains) {
			return false, nil
		}
	}

	// PR labels filter
	if len(filter.Labels) > 0 {
		if event.PullRequest == nil {
			return false, nil
		}
		if !hasAllLabels(event.PullRequest.Labels, filter.Labels) {
			return false, nil
		}
	}

	// PR state filter
	if filter.State != "" {
		if event.PullRequest == nil || getString(event.PullRequest.State) != filter.State {
			return false, nil
		}
	}

	// Draft filter
	if filter.Draft != nil {
		if event.PullRequest == nil {
			return false, nil
		}
		if event.PullRequest.Draft != nil && *event.PullRequest.Draft != *filter.Draft {
			return false, nil
		}
	}

	return true, nil
}

func matchesPushEvent(filter kelosv1alpha1.GitHubWebhookFilter, event *github.PushEvent) (bool, error) {
	// Branch filter
	if filter.Branch != "" {
		if event.Ref == nil {
			return false, nil
		}
		// Extract branch name from ref (refs/heads/main -> main)
		ref := getString(event.Ref)
		branchName := strings.TrimPrefix(ref, "refs/heads/")

		// Support glob patterns
		matched, err := filepath.Match(filter.Branch, branchName)
		if err != nil {
			return false, fmt.Errorf("invalid branch pattern %q: %w", filter.Branch, err)
		}
		if !matched {
			return false, nil
		}
	}

	return true, nil
}

// hasAllLabels checks if the issue/PR has all the required labels.
func hasAllLabels(issueLabels []*github.Label, requiredLabels []string) bool {
	labelSet := make(map[string]bool)
	for _, label := range issueLabels {
		if label.Name != nil {
			labelSet[*label.Name] = true
		}
	}

	for _, required := range requiredLabels {
		if !labelSet[required] {
			return false
		}
	}
	return true
}

// getString safely extracts a string from a GitHub SDK string pointer.
func getString(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

// ExtractGitHubWorkItem extracts template variables from a GitHub webhook payload.
// Returns a map suitable for use in prompt template rendering.
func ExtractGitHubWorkItem(eventType string, payload []byte) map[string]interface{} {
	// Parse the webhook using go-github
	data, err := ParseGitHubWebhook(eventType, payload)
	if err != nil {
		return nil
	}

	return ExtractTemplateVars(data)
}

// ExtractTemplateVars extracts template variables from GitHub webhook data for use in
// promptTemplate and branch rendering. Returns variables compatible with existing
// template format plus new webhook-specific ones.
func ExtractTemplateVars(data *GitHubEventData) map[string]interface{} {
	vars := map[string]interface{}{
		"Event":   data.Event,
		"Action":  data.Action,
		"Payload": data.RawPayload, // Full payload access for {{.Payload.field.sub}}
	}

	// Add sender info
	if data.Sender != nil {
		vars["Sender"] = getString(data.Sender.Login)
	}

	// Add ref for push events
	if pushEvent, ok := data.RawEvent.(*github.PushEvent); ok && pushEvent.Ref != nil {
		vars["Ref"] = getString(pushEvent.Ref)
	}

	// Extract common fields based on event type for backward compatibility
	switch e := data.RawEvent.(type) {
	case *github.IssueCommentEvent:
		if e.Issue != nil {
			vars["ID"] = fmt.Sprintf("issue-%d", getInt(e.Issue.Number))
			vars["Number"] = getInt(e.Issue.Number)
			vars["Title"] = getString(e.Issue.Title)
			vars["Body"] = getString(e.Issue.Body)
			vars["URL"] = getString(e.Issue.HTMLURL)
			vars["Kind"] = "Issue"
			if e.Issue.PullRequestLinks != nil {
				vars["Kind"] = "PR"
			}
			vars["Labels"] = extractLabelNames(e.Issue.Labels)
		}
		if e.Comment != nil {
			vars["Body"] = getString(e.Comment.Body) // Override with comment body
		}

	case *github.IssuesEvent:
		if e.Issue != nil {
			vars["ID"] = fmt.Sprintf("issue-%d", getInt(e.Issue.Number))
			vars["Number"] = getInt(e.Issue.Number)
			vars["Title"] = getString(e.Issue.Title)
			vars["Body"] = getString(e.Issue.Body)
			vars["URL"] = getString(e.Issue.HTMLURL)
			vars["Kind"] = "Issue"
			vars["Labels"] = extractLabelNames(e.Issue.Labels)
		}

	case *github.PullRequestEvent:
		if e.PullRequest != nil {
			vars["ID"] = fmt.Sprintf("pr-%d", getInt(e.PullRequest.Number))
			vars["Number"] = getInt(e.PullRequest.Number)
			vars["Title"] = getString(e.PullRequest.Title)
			vars["Body"] = getString(e.PullRequest.Body)
			vars["URL"] = getString(e.PullRequest.HTMLURL)
			vars["Kind"] = "PR"
			vars["Labels"] = extractLabelNames(e.PullRequest.Labels)
		}

	case *github.PullRequestReviewEvent:
		if e.PullRequest != nil {
			vars["ID"] = fmt.Sprintf("pr-%d", getInt(e.PullRequest.Number))
			vars["Number"] = getInt(e.PullRequest.Number)
			vars["Title"] = getString(e.PullRequest.Title)
			vars["URL"] = getString(e.PullRequest.HTMLURL)
			vars["Kind"] = "PR"
			vars["Labels"] = extractLabelNames(e.PullRequest.Labels)
		}
		if e.Review != nil {
			vars["Body"] = getString(e.Review.Body) // Review body, not PR body
		}

	case *github.PullRequestReviewCommentEvent:
		if e.PullRequest != nil {
			vars["ID"] = fmt.Sprintf("pr-%d", getInt(e.PullRequest.Number))
			vars["Number"] = getInt(e.PullRequest.Number)
			vars["Title"] = getString(e.PullRequest.Title)
			vars["URL"] = getString(e.PullRequest.HTMLURL)
			vars["Kind"] = "PR"
			vars["Labels"] = extractLabelNames(e.PullRequest.Labels)
		}
		if e.Comment != nil {
			vars["Body"] = getString(e.Comment.Body) // Comment body
		}

	case *github.PushEvent:
		// For push events, create a synthetic ID from the head commit
		if e.HeadCommit != nil {
			vars["ID"] = fmt.Sprintf("push-%s", getString(e.HeadCommit.ID)[:8])
			vars["Title"] = getString(e.HeadCommit.Message)
			vars["Body"] = getString(e.HeadCommit.Message)
			vars["URL"] = getString(e.HeadCommit.URL)
		} else if e.After != nil {
			commitID := getString(e.After)
			if len(commitID) >= 8 {
				vars["ID"] = fmt.Sprintf("push-%s", commitID[:8])
			} else {
				vars["ID"] = fmt.Sprintf("push-%s", commitID)
			}
		}
		vars["Kind"] = "Push"
		vars["Number"] = 0 // Push events don't have numbers
	}

	return vars
}

// extractLabelNames extracts label names from GitHub label objects.
func extractLabelNames(labels []*github.Label) []string {
	names := make([]string, 0, len(labels))
	for _, label := range labels {
		if label.Name != nil {
			names = append(names, *label.Name)
		}
	}
	return names
}

// getInt safely extracts an int from a GitHub SDK int pointer.
func getInt(ptr *int) int {
	if ptr == nil {
		return 0
	}
	return *ptr
}
