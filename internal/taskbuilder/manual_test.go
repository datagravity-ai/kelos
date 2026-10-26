package taskbuilder

import (
	"strings"
	"testing"
)

func TestManualTaskNameFitsDNSLabel(t *testing.T) {
	name := manualTaskName(strings.Repeat("a", 63), "abcde")
	if len(name) > 63 {
		t.Fatalf("name length = %d, want at most 63", len(name))
	}
	if !strings.HasSuffix(name, "-manual-abcde") {
		t.Errorf("name = %q, want manual suffix", name)
	}
}
