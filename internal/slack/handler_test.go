package slack

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	goslack "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

type fakeSlackAPI struct {
	convInfo      *goslack.Channel
	convInfoErr   error
	leftChannels  []string
	postedMsgs    []fakePostedMsg
	leaveErr      error
}

type fakePostedMsg struct {
	channel string
}

func (f *fakeSlackAPI) GetConversationInfoContext(_ context.Context, input *goslack.GetConversationInfoInput) (*goslack.Channel, error) {
	return f.convInfo, f.convInfoErr
}

func (f *fakeSlackAPI) LeaveConversationContext(_ context.Context, channelID string) (bool, error) {
	f.leftChannels = append(f.leftChannels, channelID)
	return true, f.leaveErr
}

func (f *fakeSlackAPI) PostMessageContext(_ context.Context, channelID string, _ ...goslack.MsgOption) (string, string, error) {
	f.postedMsgs = append(f.postedMsgs, fakePostedMsg{channel: channelID})
	return "", "", nil
}

func (f *fakeSlackAPI) GetUserInfoContext(_ context.Context, _ string) (*goslack.User, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeSlackAPI) GetPermalinkContext(_ context.Context, _ *goslack.PermalinkParameters) (string, error) {
	return "", errors.New("not implemented")
}

func (f *fakeSlackAPI) GetConversationRepliesContext(_ context.Context, _ *goslack.GetConversationRepliesParameters) ([]goslack.Message, bool, string, error) {
	return nil, false, "", errors.New("not implemented")
}

// TestRouteMessageThreadContextBody verifies that routeMessage preserves the
// thread context body for thread replies (HasThreadContext=true) and uses the
// trigger-processed body for top-level messages.
func TestRouteMessageThreadContextBody(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	spawner := &v1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
			UID:       "spawner-uid",
		},
		Spec: v1alpha1.TaskSpawnerSpec{
			When: v1alpha1.When{
				Slack: &v1alpha1.Slack{
					MentionUserIDs: []string{"UBOT"},
					TriggerCommand: "/solve",
				},
			},
			TaskTemplate: v1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: v1alpha1.Credentials{
					Type: v1alpha1.CredentialTypeNone,
				},
				PromptTemplate: "{{.Body}}",
			},
		},
	}

	tests := []struct {
		name     string
		msg      *SlackMessageData
		wantBody string
	}{
		{
			name: "top-level message uses trigger-processed body",
			msg: &SlackMessageData{
				UserID:    "U1",
				ChannelID: "C1",
				Text:      "<@UBOT> /solve fix the bug",
				Body:      "<@UBOT> /solve fix the bug",
				Timestamp: "1111111111.111111",
			},
			// TriggerCommand="/solve" strips the prefix, leaving just "fix the bug"
			wantBody: "fix the bug",
		},
		{
			name: "thread reply with context preserves thread body",
			msg: &SlackMessageData{
				UserID:           "U1",
				ChannelID:        "C1",
				Text:             "<@UBOT> /solve can you take a look",
				Body:             "Slack thread conversation:\n\nUser: original question\n\nUser: <@UBOT> /solve can you take a look\n",
				ThreadTS:         "1111111111.000000",
				Timestamp:        "2222222222.222222",
				HasThreadContext: true,
			},
			// HasThreadContext=true means the thread body is preserved as-is
			wantBody: "Slack thread conversation:\n\nUser: original question\n\nUser: <@UBOT> /solve can you take a look\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(spawner.DeepCopy()).
				Build()

			tb, err := taskbuilder.NewTaskBuilder(cl)
			if err != nil {
				t.Fatalf("NewTaskBuilder: %v", err)
			}

			h := &SlackHandler{
				client:      cl,
				log:         logr.Discard(),
				taskBuilder: tb,
				botUserID:   "UBOT",
			}

			h.routeMessage(context.Background(), tt.msg)

			// Verify a task was created with the expected body
			var tasks v1alpha1.TaskList
			if err := cl.List(context.Background(), &tasks); err != nil {
				t.Fatalf("List tasks: %v", err)
			}
			if len(tasks.Items) != 1 {
				t.Fatalf("Expected 1 task, got %d", len(tasks.Items))
			}
			if tasks.Items[0].Spec.Prompt != tt.wantBody {
				t.Errorf("Task prompt = %q, want %q", tasks.Items[0].Spec.Prompt, tt.wantBody)
			}
		})
	}
}

// TestShouldProcessAppMention verifies the filtering logic for app_mention events.
func TestShouldProcessAppMention(t *testing.T) {
	tests := []struct {
		name       string
		event      *slackevents.AppMentionEvent
		selfUserID string
		want       bool
	}{
		{
			name: "workflow mention is processed",
			event: &slackevents.AppMentionEvent{
				User:  "UWORKFLOW",
				Text:  "<@UBOT> please review",
				BotID: "B_WORKFLOW",
			},
			selfUserID: "UBOT",
			want:       true,
		},
		{
			name: "regular user mention is skipped",
			event: &slackevents.AppMentionEvent{
				User: "U_REGULAR",
				Text: "<@UBOT> hello",
			},
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name: "self mention is skipped",
			event: &slackevents.AppMentionEvent{
				User:  "UBOT",
				Text:  "<@UBOT> self mention",
				BotID: "B_SELF",
			},
			selfUserID: "UBOT",
			want:       false,
		},
		{
			name: "empty text is skipped",
			event: &slackevents.AppMentionEvent{
				User:  "UWORKFLOW",
				Text:  "",
				BotID: "B_WORKFLOW",
			},
			selfUserID: "UBOT",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldProcessAppMention(tt.event, tt.selfUserID)
			if got != tt.want {
				t.Errorf("shouldProcessAppMention() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRouteMessageFromAppMention verifies that a message originating from a
// workflow app_mention (routed as SlackMessageData) creates a task.
func TestRouteMessageFromAppMention(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	spawner := &v1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
			UID:       "spawner-uid",
		},
		Spec: v1alpha1.TaskSpawnerSpec{
			When: v1alpha1.When{
				Slack: &v1alpha1.Slack{
					MentionUserIDs: []string{"UBOT"},
				},
			},
			TaskTemplate: v1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: v1alpha1.Credentials{
					Type: v1alpha1.CredentialTypeNone,
				},
				PromptTemplate: "{{.Body}}",
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(spawner.DeepCopy()).
		Build()

	tb, err := taskbuilder.NewTaskBuilder(cl)
	if err != nil {
		t.Fatalf("NewTaskBuilder: %v", err)
	}

	h := &SlackHandler{
		client:      cl,
		log:         logr.Discard(),
		taskBuilder: tb,
		botUserID:   "UBOT",
	}

	// Simulate a message that would come from enrichAppMention after a workflow
	// app_mention event.
	msg := &SlackMessageData{
		UserID:    "UWORKFLOW",
		ChannelID: "C1",
		UserName:  "workflow-bot",
		Text:      "<@UBOT> please review this PR",
		Body:      "<@UBOT> please review this PR",
		Timestamp: "1111111111.111111",
	}

	h.routeMessage(context.Background(), msg)

	var tasks v1alpha1.TaskList
	if err := cl.List(context.Background(), &tasks); err != nil {
		t.Fatalf("List tasks: %v", err)
	}
	if len(tasks.Items) != 1 {
		t.Fatalf("Expected 1 task from workflow app_mention, got %d", len(tasks.Items))
	}
	if tasks.Items[0].Spec.Prompt != "<@UBOT> please review this PR" {
		t.Errorf("Task prompt = %q, want %q", tasks.Items[0].Spec.Prompt, "<@UBOT> please review this PR")
	}
}

func TestCreateTaskAlreadyExists(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	spawner := &v1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
			UID:       "spawner-uid",
		},
		Spec: v1alpha1.TaskSpawnerSpec{
			TaskTemplate: v1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: v1alpha1.Credentials{
					Type: v1alpha1.CredentialTypeNone,
				},
				PromptTemplate: "{{.Body}}",
			},
		},
	}

	msg := &SlackMessageData{
		UserID:    "U123",
		ChannelID: "C456",
		Text:      "hello",
		Body:      "hello",
		Timestamp: "1234567890.123456",
	}

	tb, err := taskbuilder.NewTaskBuilder(nil)
	if err != nil {
		t.Fatalf("NewTaskBuilder: %v", err)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	h := &SlackHandler{
		client:      cl,
		log:         logr.Discard(),
		taskBuilder: tb,
	}

	// First call should succeed
	if err := h.createTask(context.Background(), spawner, msg); err != nil {
		t.Fatalf("First createTask() error: %v", err)
	}

	// Verify Slack user ID annotation is set
	taskList := &v1alpha1.TaskList{}
	if err := cl.List(context.Background(), taskList); err != nil {
		t.Fatalf("List tasks: %v", err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(taskList.Items))
	}
	got := taskList.Items[0].Annotations[reporting.AnnotationSlackUserID]
	if got != "U123" {
		t.Errorf("Expected slack-user-id annotation %q, got %q", "U123", got)
	}

	// Second call with same message should not return an error (AlreadyExists is handled)
	if err := h.createTask(context.Background(), spawner, msg); err != nil {
		t.Fatalf("Second createTask() should not error on AlreadyExists, got: %v", err)
	}
}

func TestReadJoinMessage(t *testing.T) {
	tests := []struct {
		name        string
		fileContent string
		noFile      bool
		wantMsg     string
		wantErr     bool
	}{
		{
			name:        "reads and trims message from file",
			fileContent: "Hello! I'm Kelos bot. Mention me to get started.\n",
			wantMsg:     "Hello! I'm Kelos bot. Mention me to get started.",
		},
		{
			name:    "empty file path returns empty string",
			noFile:  true,
			wantMsg: "",
		},
		{
			name:        "empty file content returns empty string",
			fileContent: "   \n",
			wantMsg:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &SlackHandler{log: logr.Discard()}

			if !tt.noFile {
				dir := t.TempDir()
				path := filepath.Join(dir, "join-message.txt")
				if err := os.WriteFile(path, []byte(tt.fileContent), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				h.joinMessageFile = path
			}

			got, err := h.readJoinMessage()
			if (err != nil) != tt.wantErr {
				t.Fatalf("readJoinMessage() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.wantMsg {
				t.Errorf("readJoinMessage() = %q, want %q", got, tt.wantMsg)
			}
		})
	}
}

func TestReadJoinMessageMissingFile(t *testing.T) {
	h := &SlackHandler{
		log:             logr.Discard(),
		joinMessageFile: "/nonexistent/path/join-message.txt",
	}

	_, err := h.readJoinMessage()
	if err == nil {
		t.Fatal("Expected error for missing file, got nil")
	}
}

func TestHandleMemberJoinedChannelIgnoresOtherUsers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "join-message.txt")
	if err := os.WriteFile(path, []byte("Welcome!"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	h := &SlackHandler{
		log:             logr.Discard(),
		botUserID:       "UBOT",
		joinMessageFile: path,
		// api is nil — if handleMemberJoinedChannel tries to post for a
		// non-bot user it will panic, which is the desired failure mode here.
	}

	evt := &slackevents.MemberJoinedChannelEvent{
		User:    "UOTHER",
		Channel: "C123",
	}

	// Should return without attempting to post (no panic = pass).
	h.handleMemberJoinedChannel(context.Background(), evt)
}

func TestHandleMemberJoinedChannel_ExternalChannel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "join-message.txt")
	if err := os.WriteFile(path, []byte("Welcome!"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tests := []struct {
		name          string
		convInfo      *goslack.Channel
		convInfoErr   error
		wantLeave     bool
		wantGreeting  bool
	}{
		{
			name: "external channel leaves and no greeting",
			convInfo: &goslack.Channel{
				GroupConversation: goslack.GroupConversation{
					Conversation: goslack.Conversation{
						IsExtShared: true,
					},
				},
			},
			wantLeave:    true,
			wantGreeting: false,
		},
		{
			name: "pending external channel leaves and no greeting",
			convInfo: &goslack.Channel{
				GroupConversation: goslack.GroupConversation{
					Conversation: goslack.Conversation{
						IsPendingExtShared: true,
					},
				},
			},
			wantLeave:    true,
			wantGreeting: false,
		},
		{
			name: "internal channel posts greeting",
			convInfo: &goslack.Channel{
				GroupConversation: goslack.GroupConversation{
					Conversation: goslack.Conversation{
						IsExtShared:        false,
						IsPendingExtShared: false,
					},
				},
			},
			wantLeave:    false,
			wantGreeting: true,
		},
		{
			name:         "conversations.info error fails open with greeting",
			convInfoErr:  errors.New("api error"),
			wantLeave:    false,
			wantGreeting: true,
		},
		{
			name:         "nil channel with nil error fails open with greeting",
			convInfo:     nil,
			wantLeave:    false,
			wantGreeting: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeSlackAPI{
				convInfo:    tt.convInfo,
				convInfoErr: tt.convInfoErr,
			}

			h := &SlackHandler{
				log:             logr.Discard(),
				botUserID:       "UBOT",
				joinMessageFile: path,
				api:             fake,
			}

			evt := &slackevents.MemberJoinedChannelEvent{
				User:    "UBOT",
				Channel: "C123",
			}

			h.handleMemberJoinedChannel(context.Background(), evt)

			if tt.wantLeave {
				if len(fake.leftChannels) != 1 || fake.leftChannels[0] != "C123" {
					t.Errorf("Expected leave on channel C123, got %v", fake.leftChannels)
				}
			} else {
				if len(fake.leftChannels) != 0 {
					t.Errorf("Expected no leave, got %v", fake.leftChannels)
				}
			}

			if tt.wantGreeting {
				if len(fake.postedMsgs) != 1 || fake.postedMsgs[0].channel != "C123" {
					t.Errorf("Expected greeting posted to C123, got %v", fake.postedMsgs)
				}
			} else {
				if len(fake.postedMsgs) != 0 {
					t.Errorf("Expected no greeting, got %v", fake.postedMsgs)
				}
			}
		})
	}
}
