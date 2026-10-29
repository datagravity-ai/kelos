package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestCreateAgentConfigWithSkillDirectory(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"SKILL.md":                       "# Review\nRead references/nested/checklist.md\n",
		"references/nested/checklist.md": "Check compatibility\n\n",
		"scripts/check.sh":               "#!/bin/sh\nprintf 'checked\\n'\n",
		".metadata":                      "included\n",
	} {
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "scripts/check.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(config, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"create", "agentconfig", "reviewer", "--config", config, "--dry-run", "--namespace", "default", "--skill", "review=@" + dir})
	var runErr error
	output := captureStdout(t, func() {
		runErr = cmd.Execute()
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	var ac kelos.AgentConfig
	if err := yaml.Unmarshal([]byte(output), &ac); err != nil {
		t.Fatal(err)
	}
	want := kelos.SkillDefinition{
		Name: "review", Content: "# Review\nRead references/nested/checklist.md\n",
		Files: []kelos.SkillFile{
			{Path: ".metadata", Content: "included\n"},
			{Path: "references/nested/checklist.md", Content: "Check compatibility\n\n"},
			{Path: "scripts/check.sh", Content: "#!/bin/sh\nprintf 'checked\\n'\n", Executable: true},
		},
	}
	if ac.Name != "reviewer" || len(ac.Spec.Plugins) != 1 || len(ac.Spec.Plugins[0].Skills) != 1 {
		t.Fatalf("unexpected AgentConfig: %#v", ac)
	}
	if got := ac.Spec.Plugins[0].Skills[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("skill = %#v, want %#v", got, want)
	}
}

func TestParseSkillFlag(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("file content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	for _, value := range []string{"review=file content", "review=@" + filepath.Join(dir, "SKILL.md"), "review=@~/SKILL.md"} {
		t.Run(value, func(t *testing.T) {
			got, err := parseSkillFlag(value)
			if err != nil {
				t.Fatal(err)
			}
			if want := (kelos.SkillDefinition{Name: "review", Content: "file content"}); !reflect.DeepEqual(got, want) {
				t.Errorf("skill = %#v, want %#v", got, want)
			}
		})
	}
	got, err := parseSkillFlag("review=@~")
	if err != nil || got.Content != "file content\n" {
		t.Fatalf("directory with home expansion = %#v, %v", got, err)
	}
}

func TestParseSkillDirectoryErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  string
	}{
		{name: "missing SKILL.md", want: "must contain SKILL.md"},
		{name: "binary", want: "UTF-8 text", setup: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "asset.bin"), []byte{0xff}, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", want: "symbolic links are not supported", setup: func(t *testing.T, dir string) {
			if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(dir, "linked")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			_, err := parseSkillFlag("review=@" + dir)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseSkillFlagHomeErrorNamesSkill(t *testing.T) {
	t.Setenv("HOME", "")
	_, err := parseSkillFlag("review=@~/skills/review")
	if err == nil || !strings.Contains(err.Error(), `resolving --skill "review"`) {
		t.Fatalf("error = %v, want the failing skill name", err)
	}
}

func TestParseSkillFlagSymlinkedDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references/check.md"), []byte("Checklist\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "review")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	want := kelos.SkillDefinition{Name: "review", Content: "# Review\n", Files: []kelos.SkillFile{{Path: "references/check.md", Content: "Checklist\n"}}}
	for _, root := range []string{link, link + string(os.PathSeparator)} {
		got, err := parseSkillFlag("review=@" + root)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("skill = %#v, want %#v", got, want)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "references/check.md"), filepath.Join(dir, "linked-reference")); err != nil {
		t.Fatal(err)
	}
	_, err := parseSkillFlag("review=@" + link)
	if err == nil || !strings.Contains(err.Error(), "symbolic links are not supported") {
		t.Fatalf("error = %v, want nested symlink rejection", err)
	}
}

func TestParseSkillDirectoryContentBudget(t *testing.T) {
	for _, fileSize := range []int{kelos.MaxInlinePluginContentBytes - 1, kelos.MaxInlinePluginContentBytes, kelos.MaxInlinePluginContentBytes + 1} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "reference.md")
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(file, int64(fileSize)); err != nil {
			t.Fatal(err)
		}
		skill, err := parseSkillFlag("review=@" + dir)
		if fileSize+1 <= kelos.MaxInlinePluginContentBytes {
			if err != nil || len(skill.Files) != 1 || len(skill.Files[0].Content) != fileSize {
				t.Fatalf("directory at content budget: error = %v", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), "review") || !strings.Contains(err.Error(), "content budget") {
			t.Fatalf("file size %d: error = %v, want a named content budget error", fileSize, err)
		}
	}
}
