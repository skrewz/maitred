package forgejo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixtures for the canned-prompt config tests
// (§forgejo/config/loading, §forgejo/config/validation).

// validConfigYAML is a complete, valid engine config: every action the
// decision function references has a prompt, persona, and timeout
// (§forgejo/config/validation).
const validConfigYAML = `
org: example-org
actions:
  implement:
    prompt: "/issue-implementer {{.IssueURL}}"
    persona: s-autonomics-implementer
    timeout: 2h
  reassess:
    prompt: "/issue-reassess {{.IssueURL}}"
    persona: s-autonomics-implementer
    timeout: 1h
  review:
    prompt: "/pr-reviewer {{.PRURL}}"
    persona: s-autonomics-reviewer
    timeout: 1h
  re-review:
    prompt: "/pr-reviewer {{.PRURL}} (re-review)"
    persona: s-autonomics-reviewer
    timeout: 1h
  fix-feedback:
    prompt: "/pr-feedback-fixer {{.PRURL}}"
    persona: s-autonomics-implementer
    timeout: 2h
  merge-or-wait:
    prompt: "/pr-merger {{.PRURL}}"
    persona: s-autonomics-reviewer
    timeout: 30m
  rebase:
    prompt: "/pr-rebase {{.PRURL}}"
    persona: s-autonomics-implementer
    timeout: 30m
`

// writeConfig writes content to a file named name in dir and returns the
// file path.
func writeConfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// loadValid loads a config from a fresh temp file holding validConfigYAML.
func loadValid(t *testing.T) *Config {
	t.Helper()
	dir := t.TempDir()
	path := writeConfig(t, dir, "forgejoeng.yaml", validConfigYAML)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(valid): %v", err)
	}
	return cfg
}

// TestLoadConfigFile loads a valid config from a single file and checks
// the parsed values (§forgejo/config/loading,
// §forgejo/config/canned-prompts).
func TestLoadConfigFile(t *testing.T) {
	cfg := loadValid(t)

	if cfg.Org != "example-org" {
		t.Errorf("org = %q, want %q", cfg.Org, "example-org")
	}
	if len(cfg.Actions) != len(AllActions) {
		t.Fatalf("loaded %d actions, want %d", len(cfg.Actions), len(AllActions))
	}
	implement := cfg.Actions[string(ActionImplement)]
	if implement.Prompt != "/issue-implementer {{.IssueURL}}" {
		t.Errorf("implement prompt = %q", implement.Prompt)
	}
	if implement.Persona != "s-autonomics-implementer" {
		t.Errorf("implement persona = %q", implement.Persona)
	}
	if implement.Timeout != 2*time.Hour {
		t.Errorf("implement timeout = %s, want 2h", implement.Timeout)
	}
	merge := cfg.Actions[string(ActionMergeOrWait)]
	if merge.Timeout != 30*time.Minute {
		t.Errorf("merge-or-wait timeout = %s, want 30m", merge.Timeout)
	}
}

// TestLoadConfigDirectory loads a config from a directory of files
// merged in sorted order (§forgejo/config/loading).
func TestLoadConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "01-org.yaml", "org: example-org\n")
	writeConfig(t, dir, "02-implement.yaml", `
actions:
  implement:
    prompt: "/issue-implementer {{.IssueURL}}"
    persona: s-autonomics-implementer
    timeout: 2h
`)
	// The remaining actions, split across two files.
	writeConfig(t, dir, "03-rest.yaml", strings.Replace(validConfigYAML,
		"  implement:\n    prompt: \"/issue-implementer {{.IssueURL}}\"\n    persona: s-autonomics-implementer\n    timeout: 2h\n", "", 1))

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig(dir): %v", err)
	}
	if cfg.Org != "example-org" {
		t.Errorf("org = %q, want %q", cfg.Org, "example-org")
	}
	if len(cfg.Actions) != len(AllActions) {
		t.Errorf("loaded %d actions, want %d", len(cfg.Actions), len(AllActions))
	}
}

// TestLoadConfigMissingFile reports an error for a path that does not
// exist (§forgejo/config/loading).
func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("LoadConfig(missing): expected error, got nil")
	}
}

// TestLoadConfigMalformedYAML reports an error for a file that is not
// valid YAML (§forgejo/config/loading).
func TestLoadConfigMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "forgejoeng.yaml", "org: [unclosed")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig(malformed): expected error, got nil")
	}
}

// TestLoadConfigValidation covers every load-time validation failure
// (§forgejo/config/validation).
func TestLoadConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(yaml string) string
		wantSub string
	}{
		{
			name: "missing action",
			mutate: func(y string) string {
				return strings.Replace(y, "  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n    persona: s-autonomics-implementer\n    timeout: 30m\n", "", 1)
			},
			wantSub: "rebase",
		},
		{
			name: "empty prompt",
			mutate: func(y string) string {
				return strings.Replace(y, "prompt: \"/pr-rebase {{.PRURL}}\"", "prompt: \"\"", 1)
			},
			wantSub: "prompt",
		},
		{
			name: "missing persona",
			mutate: func(y string) string {
				return strings.Replace(y, "  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n    persona: s-autonomics-implementer\n", "  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n", 1)
			},
			wantSub: "persona",
		},
		{
			name: "missing timeout",
			mutate: func(y string) string {
				return strings.Replace(y, "  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n    persona: s-autonomics-implementer\n    timeout: 30m\n", "  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n    persona: s-autonomics-implementer\n", 1)
			},
			wantSub: "timeout",
		},
		{
			name:    "zero timeout",
			mutate:  func(y string) string { return strings.Replace(y, "timeout: 30m", "timeout: 0s", 1) },
			wantSub: "timeout",
		},
		{
			name: "unparseable template",
			mutate: func(y string) string {
				return strings.Replace(y, "prompt: \"/pr-rebase {{.PRURL}}\"", "prompt: \"/pr-rebase {{.PRURL\"", 1)
			},
			wantSub: "template",
		},
		{
			name: "unknown placeholder",
			mutate: func(y string) string {
				return strings.Replace(y, "prompt: \"/pr-rebase {{.PRURL}}\"", "prompt: \"/pr-rebase {{.Bogus}}\"", 1)
			},
			wantSub: "template",
		},
		{
			name:    "unknown action",
			mutate:  func(y string) string { return y + "  bogus:\n    prompt: x\n    persona: p\n    timeout: 1h\n" },
			wantSub: "bogus",
		},
		{
			name:    "missing org",
			mutate:  func(y string) string { return strings.Replace(y, "org: example-org\n", "", 1) },
			wantSub: "org",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeConfig(t, dir, "forgejoeng.yaml", tc.mutate(validConfigYAML))
			if _, err := LoadConfig(path); err == nil {
				t.Fatalf("LoadConfig: expected error for %s", tc.name)
			} else if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

// TestLoadConfigDirectoryConflicts covers the directory-merge validation
// failures: a duplicate action and a conflicting org
// (§forgejo/config/validation).
func TestLoadConfigDirectoryConflicts(t *testing.T) {
	t.Run("duplicate action", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, "01-a.yaml", "org: example-org\nactions:\n  implement:\n    prompt: a\n    persona: p\n    timeout: 1h\n")
		writeConfig(t, dir, "02-b.yaml", "actions:\n  implement:\n    prompt: b\n    persona: p\n    timeout: 1h\n")
		if _, err := LoadConfig(dir); err == nil || !strings.Contains(err.Error(), "implement") {
			t.Fatalf("expected duplicate-action error mentioning implement, got %v", err)
		}
	})
	t.Run("conflicting org", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, "01-a.yaml", "org: one\n")
		writeConfig(t, dir, "02-b.yaml", "org: two\n")
		if _, err := LoadConfig(dir); err == nil || !strings.Contains(err.Error(), "org") {
			t.Fatalf("expected conflicting-org error, got %v", err)
		}
	})
}

// TestPromptFor looks up a prompt by action
// (§forgejo/config/canned-prompts).
func TestPromptFor(t *testing.T) {
	cfg := loadValid(t)
	pc, err := cfg.PromptFor(ActionReview)
	if err != nil {
		t.Fatalf("PromptFor(review): %v", err)
	}
	if pc.Persona != "s-autonomics-reviewer" {
		t.Errorf("persona = %q", pc.Persona)
	}
	if _, err := cfg.PromptFor(Action("nope")); err == nil {
		t.Error("PromptFor(unknown): expected error, got nil")
	}
}

// TestRenderPlaceholders fills every placeholder from the table and
// checks the rendered prompt (§forgejo/config/placeholders).
func TestRenderPlaceholders(t *testing.T) {
	cfg := loadValid(t)
	pc, err := cfg.PromptFor(ActionImplement)
	if err != nil {
		t.Fatalf("PromptFor(implement): %v", err)
	}
	got, err := pc.Render(Placeholders{
		Repo:     "o/r",
		Kind:     "issue",
		Number:   7,
		IssueURL: "https://forge.example.com/o/r/issues/7",
		Sender:   "alice",
		Action:   "implement",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "/issue-implementer https://forge.example.com/o/r/issues/7"
	if got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}

	// A template using the full placeholder table renders every value.
	full := &PromptConfig{
		Prompt:  "{{.Repo}}/{{.Kind}}/{{.Number}} {{.IssueURL}} {{.PRURL}} {{.Sender}} {{.Action}}",
		Persona: "p",
		Timeout: time.Hour,
	}
	got, err = full.Render(Placeholders{
		Repo:   "o/r",
		Kind:   "pr",
		Number: 9,
		PRURL:  "https://forge.example.com/o/r/pulls/9",
		Sender: "bob",
		Action: "review",
	})
	if err != nil {
		t.Fatalf("Render(full): %v", err)
	}
	want = "o/r/pr/9  https://forge.example.com/o/r/pulls/9 bob review"
	if got != want {
		t.Errorf("Render(full) = %q, want %q", got, want)
	}
}

// TestAllActions pins the actions the config must cover: the decision
// function's action enum (§forgejo/config/validation,
// §forgejo/decisions/actions).
func TestAllActions(t *testing.T) {
	want := []Action{
		ActionImplement, ActionReassess, ActionReview, ActionReReview,
		ActionFixFeedback, ActionMergeOrWait, ActionRebase,
	}
	if len(AllActions) != len(want) {
		t.Fatalf("AllActions has %d entries, want %d", len(AllActions), len(want))
	}
	for i, a := range want {
		if AllActions[i] != a {
			t.Errorf("AllActions[%d] = %q, want %q", i, AllActions[i], a)
		}
	}
}
