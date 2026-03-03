package cli

import (
	"strings"
	"testing"
)

func TestParseSkillsShFlag(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantSource string
		wantSkill  string
		wantErr    bool
		wantErrStr string
	}{
		{
			name:       "source only",
			input:      "vercel-labs/agent-skills",
			wantSource: "vercel-labs/agent-skills",
		},
		{
			name:       "source with skill",
			input:      "vercel-labs/agent-skills:deploy",
			wantSource: "vercel-labs/agent-skills",
			wantSkill:  "deploy",
		},
		{
			name:       "empty input",
			input:      "",
			wantErr:    true,
			wantErrStr: "must not be empty",
		},
		{
			name:       "empty skill after colon",
			input:      "vercel-labs/agent-skills:",
			wantErr:    true,
			wantErrStr: "skill name after colon must not be empty",
		},
		{
			name:       "empty source with skill",
			input:      ":myskill",
			wantErr:    true,
			wantErrStr: "source must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := parseSkillsShFlag(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrStr)
				}
				if tt.wantErrStr != "" && !strings.Contains(err.Error(), tt.wantErrStr) {
					t.Errorf("expected error containing %q, got: %v", tt.wantErrStr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if spec.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", spec.Source, tt.wantSource)
			}
			if spec.Skill != tt.wantSkill {
				t.Errorf("Skill = %q, want %q", spec.Skill, tt.wantSkill)
			}
		})
	}
}
