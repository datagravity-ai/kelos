package capture

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestStreamUsage(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		content   string
		want      map[string]string
	}{
		{
			name:      "claude-code result",
			agentType: "claude-code",
			content: `{"type":"assistant","message":"thinking..."}
{"type":"result","total_cost_usd":0.0532,"usage":{"input_tokens":15230,"output_tokens":4821}}
`,
			want: map[string]string{
				"cost-usd":      "0.0532",
				"input-tokens":  "15230",
				"output-tokens": "4821",
			},
		},
		{
			name:      "claude-code uses last result",
			agentType: "claude-code",
			content: `{"type":"result","total_cost_usd":0.01,"usage":{"input_tokens":100,"output_tokens":50}}
{"type":"result","total_cost_usd":0.05,"usage":{"input_tokens":200,"output_tokens":100}}
`,
			want: map[string]string{
				"cost-usd":      "0.05",
				"input-tokens":  "200",
				"output-tokens": "100",
			},
		},
		{
			name:      "claude-code captures response",
			agentType: "claude-code",
			content: `{"type":"assistant","message":"thinking..."}
{"type":"result","total_cost_usd":0.05,"result":"Here are the PRs I opened.","usage":{"input_tokens":200,"output_tokens":100}}
`,
			want: map[string]string{
				"cost-usd":      "0.05",
				"input-tokens":  "200",
				"output-tokens": "100",
				"response":      "SGVyZSBhcmUgdGhlIFBScyBJIG9wZW5lZC4=", // base64("Here are the PRs I opened.")
			},
		},
		{
			name:      "codex sums turns",
			agentType: "codex",
			content: `{"type":"turn.started"}
{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":50}}
{"type":"turn.started"}
{"type":"turn.completed","usage":{"input_tokens":200,"output_tokens":150}}
`,
			want: map[string]string{
				"input-tokens":  "300",
				"output-tokens": "200",
			},
		},
		{
			name:      "codex no turns returns nil",
			agentType: "codex",
			content:   `{"type":"something_else"}` + "\n",
			want:      nil,
		},
		{
			name:      "gemini result with stats",
			agentType: "gemini",
			content: `{"type":"progress","message":"working..."}
{"type":"result","stats":{"inputTokens":8000,"outputTokens":3200}}
`,
			want: map[string]string{
				"input-tokens":  "8000",
				"output-tokens": "3200",
			},
		},
		{
			name:      "opencode sums steps",
			agentType: "opencode",
			content: `{"type":"step_start"}
{"type":"step_finish","part":{"tokens":{"input":500,"output":200}}}
{"type":"step_start"}
{"type":"step_finish","part":{"tokens":{"input":300,"output":100}}}
`,
			want: map[string]string{
				"input-tokens":  "800",
				"output-tokens": "300",
			},
		},
		{
			name:      "cursor result with camelCase usage",
			agentType: "cursor",
			content: `{"type":"thinking","subtype":"delta","text":"working..."}
{"type":"result","subtype":"success","duration_ms":11799,"is_error":false,"result":"done","usage":{"inputTokens":36067,"outputTokens":227,"cacheReadTokens":34560,"cacheWriteTokens":0}}
`,
			want: map[string]string{
				"input-tokens":  "36067",
				"output-tokens": "227",
				"response":      "ZG9uZQ==", // base64("done")
			},
		},
		{
			name:      "unknown agent type returns nil",
			agentType: "unknown-agent",
			content:   `{"type":"result"}` + "\n",
			want:      nil,
		},
		{
			name:      "empty agent type returns nil",
			agentType: "",
			content:   `{"type":"result"}` + "\n",
			want:      nil,
		},
		{
			name:      "empty stream returns nil",
			agentType: "claude-code",
			content:   "",
			want:      nil,
		},
		{
			name:      "malformed JSON lines are skipped",
			agentType: "claude-code",
			content: `not json
{"type":"result","total_cost_usd":0.1,"usage":{"input_tokens":500,"output_tokens":200}}
`,
			want: map[string]string{
				"cost-usd":      "0.1",
				"input-tokens":  "500",
				"output-tokens": "200",
			},
		},
		{
			name:      "claude-code no result lines returns nil",
			agentType: "claude-code",
			content:   `{"type":"assistant","message":"done"}` + "\n",
			want:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := StreamUsage(tt.agentType, strings.NewReader(tt.content), &out)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertMapEqual(t, tt.want, got)
			// Every input line must be forwarded to stdout unchanged.
			assertForwarded(t, tt.content, out.String())
		})
	}
}

// TestStreamUsageForwardsUnterminatedLine verifies that an input that does
// not end with a newline still has its final line forwarded (terminated
// with a newline, matching what `tee` produced previously).
func TestStreamUsageForwardsUnterminatedLine(t *testing.T) {
	in := `{"type":"result","total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":2}}`
	var out bytes.Buffer
	got, err := StreamUsage("claude-code", strings.NewReader(in), &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["cost-usd"] != "0.01" {
		t.Errorf("cost-usd: want %q, got %q", "0.01", got["cost-usd"])
	}
	if out.String() != in+"\n" {
		t.Errorf("forwarded output mismatch:\n  want: %q\n  got:  %q", in+"\n", out.String())
	}
}

// TestStreamUsageForwardsLargeLineByteForByte verifies that an
// arbitrarily large single line is forwarded to w byte-for-byte and that
// subsequent lines are still parsed for usage. The previous
// bufio.Scanner-based implementation imposed a 10 MiB cap that either
// dropped bytes or risked SIGPIPE in the agent producer once the line
// exceeded the buffer; this test guards against any return of that
// behaviour.
func TestStreamUsageForwardsLargeLineByteForByte(t *testing.T) {
	huge := strings.Repeat("a", 11*1024*1024)
	tail := `{"type":"result","total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":2}}`
	in := huge + "\n" + tail + "\n"
	var out bytes.Buffer
	got, err := StreamUsage("claude-code", strings.NewReader(in), &out)
	if err != nil {
		t.Fatalf("unexpected error on large line: %v", err)
	}
	if out.String() != in {
		t.Errorf("forwarded output not byte-identical; got %d bytes, want %d", out.Len(), len(in))
	}
	if got["cost-usd"] != "0.01" {
		t.Errorf("usage parsing skipped on line after large line: got %v", got)
	}
}

// TestStreamUsagePropagatesFlushError verifies that a writer error
// surfaced only at the final Flush is returned to the caller, not
// silently swallowed.
func TestStreamUsagePropagatesFlushError(t *testing.T) {
	in := `{"type":"result","total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":2}}` + "\n"
	_, err := StreamUsage("claude-code", strings.NewReader(in), errWriter{})
	if err == nil {
		t.Fatalf("expected flush error, got nil")
	}
}

// errWriter always fails. Used to force a Flush error from the buffered
// writer inside StreamUsage.
type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) {
	return 0, errWriteFailed
}

var errWriteFailed = errors.New("write failed")

// assertForwarded checks that the forwarded stream is byte-for-byte
// identical to the input, with a trailing newline appended if the input
// did not already end in one. This matches what `tee` produced and
// catches reorderings, duplications, or dropped/extra lines that a mere
// substring check would miss.
func assertForwarded(t *testing.T, in, out string) {
	t.Helper()
	want := in
	if want != "" && !strings.HasSuffix(want, "\n") {
		want += "\n"
	}
	if out != want {
		t.Errorf("forwarded output mismatch:\n  want: %q\n  got:  %q", want, out)
	}
}

func TestParseResponse(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		lines     [][]byte
		want      string
	}{
		{
			name:      "claude-code result with response",
			agentType: "claude-code",
			lines: [][]byte{
				[]byte(`{"type":"assistant","message":"thinking..."}`),
				[]byte(`{"type":"result","subtype":"success","result":"I need your GitHub username to proceed.","total_cost_usd":0.05,"usage":{"input_tokens":100,"output_tokens":50}}`),
			},
			want: "I need your GitHub username to proceed.",
		},
		{
			name:      "claude-code uses last result",
			agentType: "claude-code",
			lines: [][]byte{
				[]byte(`{"type":"result","result":"first response"}`),
				[]byte(`{"type":"result","result":"second response"}`),
			},
			want: "second response",
		},
		{
			name:      "cursor result with response",
			agentType: "cursor",
			lines: [][]byte{
				[]byte(`{"type":"result","subtype":"success","result":"done","usage":{"inputTokens":100,"outputTokens":50}}`),
			},
			want: "done",
		},
		{
			name:      "no result line returns empty",
			agentType: "claude-code",
			lines: [][]byte{
				[]byte(`{"type":"assistant","message":"thinking"}`),
			},
			want: "",
		},
		{
			name:      "result without result field returns empty",
			agentType: "claude-code",
			lines: [][]byte{
				[]byte(`{"type":"result","total_cost_usd":0.05}`),
			},
			want: "",
		},
		{
			name:      "empty agent type returns empty",
			agentType: "",
			lines: [][]byte{
				[]byte(`{"type":"result","result":"hello"}`),
			},
			want: "",
		},
		{
			name:      "unknown agent type returns empty",
			agentType: "unknown-agent",
			lines: [][]byte{
				[]byte(`{"type":"result","result":"hello"}`),
			},
			want: "",
		},
		{
			name:      "nil lines returns empty",
			agentType: "claude-code",
			lines:     nil,
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseResponse(tt.agentType, tt.lines)
			if got != tt.want {
				t.Errorf("parseResponse() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsAgentError(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		lines     [][]byte
		want      bool
	}{
		{
			name:      "claude-code is_error true",
			agentType: "claude-code",
			lines: [][]byte{
				[]byte(`{"type":"result","subtype":"error","is_error":true,"result":"something went wrong"}`),
			},
			want: true,
		},
		{
			name:      "claude-code is_error false",
			agentType: "claude-code",
			lines: [][]byte{
				[]byte(`{"type":"result","subtype":"success","is_error":false,"result":"done"}`),
			},
			want: false,
		},
		{
			name:      "claude-code no is_error field",
			agentType: "claude-code",
			lines: [][]byte{
				[]byte(`{"type":"result","total_cost_usd":0.05}`),
			},
			want: false,
		},
		{
			name:      "unsupported agent type",
			agentType: "gemini",
			lines: [][]byte{
				[]byte(`{"type":"result","is_error":true}`),
			},
			want: false,
		},
		{
			name:      "empty lines",
			agentType: "claude-code",
			lines:     nil,
			want:      false,
		},
		{
			name:      "uses last result line",
			agentType: "claude-code",
			lines: [][]byte{
				[]byte(`{"type":"result","is_error":false,"result":"first"}`),
				[]byte(`{"type":"result","is_error":true,"result":"second failed"}`),
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsAgentError(tt.agentType, tt.lines)
			if got != tt.want {
				t.Errorf("IsAgentError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsAuthFailure(t *testing.T) {
	tests := []struct {
		name          string
		agentType     string
		content       string
		extraPatterns []string
		want          bool
	}{
		{
			name:      "token expired in result text",
			agentType: "claude-code",
			content:   `{"type":"result","is_error":false,"result":"The babysitter session must end — GitHub credentials expired (HTTP 401)"}` + "\n",
			want:      true,
		},
		{
			name:      "bad credentials standalone",
			agentType: "claude-code",
			content:   `{"type":"result","is_error":false,"result":"Bad credentials (HTTP 401)"}` + "\n",
			want:      true,
		},
		{
			name:      "normal successful result",
			agentType: "claude-code",
			content:   `{"type":"result","is_error":false,"result":"Task completed successfully"}` + "\n",
			want:      false,
		},
		{
			name:          "session failed with custom extra pattern",
			agentType:     "claude-code",
			content:       `{"type":"result","is_error":false,"result":"Session failed — custom auth error XYZ"}` + "\n",
			extraPatterns: []string{"custom auth error XYZ"},
			want:          true,
		},
		{
			name:      "unsupported agent type",
			agentType: "gemini",
			content:   `{"type":"result","is_error":false,"result":"Session failed — token expired (HTTP 401)"}` + "\n",
			want:      false,
		},
		{
			name:      "session failed with token expired",
			agentType: "cursor",
			content:   `{"type":"result","is_error":false,"result":"Session failed — GitHub token expired (HTTP 401)"}` + "\n",
			want:      true,
		},
		{
			name:      "auth indicator without session ending phrase",
			agentType: "claude-code",
			content:   `{"type":"result","is_error":false,"result":"Got HTTP 401 from API"}` + "\n",
			want:      false,
		},
		{
			name:      "empty result field",
			agentType: "claude-code",
			content:   `{"type":"result","is_error":false}` + "\n",
			want:      false,
		},
		{
			name:      "auth failure detected even with is_error true",
			agentType: "claude-code",
			content:   `{"type":"result","is_error":true,"result":"Session failed — GitHub token expired (HTTP 401)"}` + "\n",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lines [][]byte
			if tt.content != "" {
				for _, line := range strings.Split(tt.content, "\n") {
					if line != "" {
						lines = append(lines, []byte(line))
					}
				}
			}
			got := IsAuthFailure(tt.agentType, lines, tt.extraPatterns)
			if got != tt.want {
				t.Errorf("IsAuthFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func assertMapEqual(t *testing.T, want, got map[string]string) {
	t.Helper()
	if len(want) == 0 && len(got) == 0 {
		return
	}
	if len(want) != len(got) {
		t.Errorf("map length mismatch: want %d, got %d\n  want: %v\n  got:  %v", len(want), len(got), want, got)
		return
	}
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Errorf("missing key %q: want %q", k, wv)
			continue
		}
		if gv != wv {
			t.Errorf("key %q: want %q, got %q", k, wv, gv)
		}
	}
}
