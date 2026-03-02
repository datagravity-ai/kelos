/*
Copyright 2026 Gunju Kim

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

package main

import (
	"testing"
)

func TestExtractNote(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "standard release note",
			body: "## Description\nSome description here.\n\n## Release Note\n\n```release-note\nAdded support for configuring agent timeout\n```",
			want: "Added support for configuring agent timeout",
		},
		{
			name: "NONE release note",
			body: "## Release Note\n\n```release-note\nNONE\n```",
			want: "NONE",
		},
		{
			name: "multi-line release note",
			body: "```release-note\nFixed a bug where session cleanup would skip errored pods\nImproved error logging for failed sessions\n```",
			want: "Fixed a bug where session cleanup would skip errored pods\nImproved error logging for failed sessions",
		},
		{
			name: "missing release note block",
			body: "## Description\nJust a description, no release note block.",
			want: "",
		},
		{
			name: "empty release note block",
			body: "```release-note\n\n```",
			want: "",
		},
		{
			name: "release note with surrounding whitespace lines",
			body: "```release-note\n\nAdded a new feature\n\n```",
			want: "Added a new feature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractNote(tt.body)
			if got != tt.want {
				t.Errorf("ExtractNote() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsNone(t *testing.T) {
	tests := []struct {
		name string
		note string
		want bool
	}{
		{name: "uppercase NONE", note: "NONE", want: true},
		{name: "lowercase none", note: "none", want: true},
		{name: "mixed case None", note: "None", want: true},
		{name: "with whitespace", note: "  NONE  ", want: true},
		{name: "actual content", note: "Added support for configuring agent timeout", want: false},
		{name: "empty string", note: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNone(tt.note)
			if got != tt.want {
				t.Errorf("IsNone(%q) = %v, want %v", tt.note, got, tt.want)
			}
		})
	}
}

func TestFormatNote(t *testing.T) {
	tests := []struct {
		name string
		note string
		pr   string
		want []string
	}{
		{
			name: "single line",
			note: "Added a feature",
			pr:   "42",
			want: []string{"- Added a feature (#42)"},
		},
		{
			name: "multi-line",
			note: "Fixed bug A\nFixed bug B",
			pr:   "99",
			want: []string{"- Fixed bug A (#99)", "- Fixed bug B (#99)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatNote(tt.note, tt.pr)
			if len(got) != len(tt.want) {
				t.Fatalf("formatNote() returned %d lines, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("formatNote()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
