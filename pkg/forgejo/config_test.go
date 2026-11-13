package forgejo

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Fixtures for the canned-prompt config tests
// (§forgejo/config/loading, §forgejo/config/validation).

// validConfigYAML is a complete, valid engine config: every entry — each
// action the decision function references and each review lens — has one
// declared identity, a prompt, and a positive timeout
// (§forgejo/config/validation, §forgejo/config/identity-collapse).
const validConfigYAML = `
org: example-org
reconcile_interval: 15m
decomposer_identity: decomposer
implementer_identity: implementer
actions:
  implement:
    prompt: "/issue-implementer {{.IssueURL}}"
    identity: implementer
    timeout: 2h
  reassess:
    prompt: "/issue-reassess {{.IssueURL}}"
    identity: implementer
    timeout: 1h
  fix-feedback:
    prompt: "/pr-feedback-fixer {{.PRURL}}"
    identity: implementer
    timeout: 2h
  merge-or-wait:
    prompt: "/pr-merger {{.PRURL}}"
    identity: reviewer
    timeout: 30m
  rebase:
    prompt: "/pr-rebase {{.PRURL}}"
    identity: implementer
    timeout: 30m
  decompose:
    prompt: "/issue-decomposer {{.IssueURL}}"
    identity: implementer
    timeout: 1h
  wrap-up:
    prompt: "/issue-wrapper {{.IssueURL}}"
    identity: implementer
    timeout: 1h
  teardown-forest:
    prompt: "/forest-teardown {{.IssueURL}}"
    identity: decomposer
    timeout: 1h
  sync:
    prompt: "/set-lander {{.IssueURL}}"
    identity: implementer
    timeout: 1h
reviews:
  - identity: reviewer-minimalism
    prompt: "/pr-minimalism-reviewer {{.PRURL}}"
    timeout: 30m
  - identity: reviewer-maintainability
    prompt: "/pr-maintainability-reviewer {{.PRURL}}"
    timeout: 30m
  - identity: reviewer-security
    prompt: "/pr-security-reviewer {{.PRURL}}"
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
	if cfg.ReconcileInterval != 15*time.Minute {
		t.Errorf("reconcile_interval = %s, want 15m", cfg.ReconcileInterval)
	}
	if cfg.DecomposerIdentity != "decomposer" {
		t.Errorf("decomposer_identity = %q, want %q", cfg.DecomposerIdentity, "decomposer")
	}
	if cfg.ImplementerIdentity != "implementer" {
		t.Errorf("implementer_identity = %q, want %q", cfg.ImplementerIdentity, "implementer")
	}
	if len(cfg.Actions) != len(AllActions) {
		t.Fatalf("loaded %d actions, want %d", len(cfg.Actions), len(AllActions))
	}
	implement := cfg.Actions[string(ActionImplement)]
	if implement.Prompt != "/issue-implementer {{.IssueURL}}" {
		t.Errorf("implement prompt = %q", implement.Prompt)
	}
	if implement.Identity != "implementer" {
		t.Errorf("implement identity = %q", implement.Identity)
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
	writeConfig(t, dir, "01-org.yaml", "org: example-org\ndecomposer_identity: decomposer\nimplementer_identity: implementer\n")
	writeConfig(t, dir, "02-implement.yaml", `
actions:
  implement:
    prompt: "/issue-implementer {{.IssueURL}}"
    identity: implementer
    timeout: 2h
`)
	// The remaining actions, split across two files.
	writeConfig(t, dir, "03-rest.yaml", strings.Replace(validConfigYAML,
		"  implement:\n    prompt: \"/issue-implementer {{.IssueURL}}\"\n    identity: implementer\n    timeout: 2h\n", "", 1))

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig(dir): %v", err)
	}
	if cfg.Org != "example-org" {
		t.Errorf("org = %q, want %q", cfg.Org, "example-org")
	}
	if cfg.DecomposerIdentity != "decomposer" {
		t.Errorf("decomposer_identity = %q, want %q", cfg.DecomposerIdentity, "decomposer")
	}
	if cfg.ImplementerIdentity != "implementer" {
		t.Errorf("implementer_identity = %q, want %q", cfg.ImplementerIdentity, "implementer")
	}
	if len(cfg.Actions) != len(AllActions) {
		t.Errorf("loaded %d actions, want %d", len(cfg.Actions), len(AllActions))
	}
}

// TestLoadConfigImplementerIdentityAbsent pins that the implementer
// identity is optional: with no key configured the config loads, the
// identity is empty, and the fix-feedback ownership gate the engine
// derives from it is inert (§forgejo/config/implementer-identity,
// §forgejo/config/validation, s-git/maitred#143).
func TestLoadConfigImplementerIdentityAbsent(t *testing.T) {
	dir := t.TempDir()
	yaml := strings.Replace(validConfigYAML, "implementer_identity: implementer\n", "", 1)
	path := writeConfig(t, dir, "forgejoeng.yaml", yaml)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig without implementer_identity: %v", err)
	}
	if cfg.ImplementerIdentity != "" {
		t.Errorf("implementer_identity = %q, want empty", cfg.ImplementerIdentity)
	}
}

// TestLoadConfigDecomposerIdentityAbsent pins that the decomposer
// identity is optional: with no key configured the config loads and
// the identity is empty (§forgejo/config/decomposer-identity,
// §forgejo/config/validation).
func TestLoadConfigDecomposerIdentityAbsent(t *testing.T) {
	dir := t.TempDir()
	yaml := strings.Replace(validConfigYAML, "decomposer_identity: decomposer\n", "", 1)
	path := writeConfig(t, dir, "forgejoeng.yaml", yaml)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig without decomposer_identity: %v", err)
	}
	if cfg.DecomposerIdentity != "" {
		t.Errorf("decomposer_identity = %q, want empty", cfg.DecomposerIdentity)
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
		name            string
		mutate          func(yaml string) string
		wantSub         string
		wantNotMismatch bool
	}{
		{
			name: "missing action",
			mutate: func(y string) string {
				return strings.Replace(y, "  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n    identity: implementer\n    timeout: 30m\n", "", 1)
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
			// An entry that declares no identity is a missing required
			// field, never a schema-key mismatch and never defaulted
			// (§forgejo/config/identity-collapse).
			name: "missing identity",
			mutate: func(y string) string {
				return strings.Replace(y, "  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n    identity: implementer\n", "  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n", 1)
			},
			wantSub:         "identity",
			wantNotMismatch: true,
		},
		{
			name: "missing timeout",
			mutate: func(y string) string {
				return strings.Replace(y, "  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n    identity: implementer\n    timeout: 30m\n", "  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n    identity: implementer\n", 1)
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
			name: "unknown action",
			mutate: func(y string) string {
				return strings.Replace(y, "actions:", "actions:\n  bogus:\n    prompt: x\n    identity: p\n    timeout: 1h", 1)
			},
			wantSub: "bogus",
		},
		{
			name: "unknown top-level key",
			mutate: func(y string) string {
				return strings.Replace(y, "org: example-org\n", "org: example-org\nbogus_key: x\n", 1)
			},
			wantSub: "bogus_key",
		},
		{
			// A document that is not a mapping at all is a parse failure,
			// not a schema one (§forgejo/config/loading).
			name:    "config is not a mapping",
			mutate:  func(string) string { return "just a string\n" },
			wantSub: "parse config",
		},
		{
			// A repeated key is owned by the lenient pass, like the
			// non-mapping document above: it is a broken file, not a
			// disagreement with the binary's key set, so it names the
			// repeated key and never classifies as the
			// config-or-binary mismatch (§forgejo/config/validation).
			name: "duplicate key",
			mutate: func(y string) string {
				return strings.Replace(y, "org: example-org\n", "org: example-org\norg: other\n", 1)
			},
			wantSub:         "already defined",
			wantNotMismatch: true,
		},
		{
			name:    "missing org",
			mutate:  func(y string) string { return strings.Replace(y, "org: example-org\n", "", 1) },
			wantSub: "org",
		},
		{
			name:    "missing reconcile_interval",
			mutate:  func(y string) string { return strings.Replace(y, "reconcile_interval: 15m\n", "", 1) },
			wantSub: "reconcile_interval",
		},
		{
			name: "zero reconcile_interval",
			mutate: func(y string) string {
				return strings.Replace(y, "reconcile_interval: 15m", "reconcile_interval: 0s", 1)
			},
			wantSub: "reconcile_interval",
		},
		{
			name: "negative reconcile_interval",
			mutate: func(y string) string {
				return strings.Replace(y, "reconcile_interval: 15m", "reconcile_interval: -15m", 1)
			},
			wantSub: "reconcile_interval",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeConfig(t, dir, "forgejoeng.yaml", tc.mutate(validConfigYAML))
			if _, err := LoadConfig(path); err == nil {
				t.Fatalf("LoadConfig: expected error for %s", tc.name)
			} else {
				if !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("error %q does not mention %q", err, tc.wantSub)
				}
				if tc.wantNotMismatch && errors.Is(err, ErrActionSetMismatch) {
					t.Errorf("%s: a missing required field is not a schema-key mismatch: %v", tc.name, err)
				}
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
		writeConfig(t, dir, "01-a.yaml", "org: example-org\nactions:\n  implement:\n    prompt: a\n    identity: p\n    timeout: 1h\n")
		writeConfig(t, dir, "02-b.yaml", "actions:\n  implement:\n    prompt: b\n    identity: p\n    timeout: 1h\n")
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
	t.Run("conflicting reconcile_interval", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, "01-a.yaml", "org: example-org\nreconcile_interval: 10m\n")
		writeConfig(t, dir, "02-b.yaml", "reconcile_interval: 20m\n")
		if _, err := LoadConfig(dir); err == nil || !strings.Contains(err.Error(), "reconcile_interval") {
			t.Fatalf("expected conflicting-reconcile_interval error, got %v", err)
		}
	})
	t.Run("conflicting decomposer_identity", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, "01-a.yaml", "org: example-org\ndecomposer_identity: one\n")
		writeConfig(t, dir, "02-b.yaml", "decomposer_identity: two\n")
		if _, err := LoadConfig(dir); err == nil || !strings.Contains(err.Error(), "decomposer_identity") {
			t.Fatalf("expected conflicting-decomposer_identity error, got %v", err)
		}
	})
	t.Run("conflicting implementer_identity", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, "01-a.yaml", "org: example-org\nimplementer_identity: one\n")
		writeConfig(t, dir, "02-b.yaml", "implementer_identity: two\n")
		if _, err := LoadConfig(dir); err == nil || !strings.Contains(err.Error(), "implementer_identity") {
			t.Fatalf("expected conflicting-implementer_identity error, got %v", err)
		}
	})
}

// TestLoadConfigSentinels checks that each way loading can fail is
// recognisable with errors.Is, not by matching its text
// (§forgejo/config/loading, §forgejo/config/validation,
// §forgejo/observability/why-the-engine-is-off).
func TestLoadConfigSentinels(t *testing.T) {
	t.Run("no config at the location", func(t *testing.T) {
		_, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"))
		if !errors.Is(err, ErrConfigNotFound) {
			t.Fatalf("err = %v, want one wrapping ErrConfigNotFound", err)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("err = %v, want one wrapping fs.ErrNotExist", err)
		}
		if errors.Is(err, ErrConfigUnreadable) {
			t.Errorf("a config that is simply absent must not also be unreadable: %v", err)
		}
	})

	t.Run("config unreadable", func(t *testing.T) {
		skipUnlessAccessDenied(t)
		path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", validConfigYAML)
		if err := os.Chmod(path, 0o000); err != nil {
			t.Skipf("chmod: %v", err)
		}
		_, err := LoadConfig(path)
		if !errors.Is(err, ErrConfigUnreadable) {
			t.Fatalf("err = %v, want one wrapping ErrConfigUnreadable", err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error %q must keep the path it failed on", err)
		}
	})

	t.Run("config unparseable", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", "org: [unclosed")
		if _, err := LoadConfig(path); !errors.Is(err, ErrConfigUnreadable) {
			t.Fatalf("err = %v, want one wrapping ErrConfigUnreadable", err)
		}
	})

	t.Run("action the binary knows has no entry", func(t *testing.T) {
		_, err := LoadConfig(validateFixturePath(t, strings.Replace(validConfigYAML,
			"  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n    identity: implementer\n    timeout: 30m\n",
			"", 1)))
		if !errors.Is(err, ErrActionSetMismatch) {
			t.Fatalf("err = %v, want one wrapping ErrActionSetMismatch", err)
		}
		if !strings.Contains(err.Error(), "rebase") {
			t.Errorf("error %q must name the action it failed on", err)
		}
	})

	t.Run("action the binary does not know has an entry", func(t *testing.T) {
		_, err := LoadConfig(validateFixturePath(t, strings.Replace(validConfigYAML, "actions:", "actions:\n  bogus:\n    prompt: x\n    identity: p\n    timeout: 1h", 1)))
		if !errors.Is(err, ErrActionSetMismatch) {
			t.Fatalf("err = %v, want one wrapping ErrActionSetMismatch", err)
		}
		if !strings.Contains(err.Error(), "bogus") {
			t.Errorf("error %q must name the action it failed on", err)
		}
	})

	// A config that carries a `persona` key is the same
	// config-or-binary mismatch as an action-set disagreement, so a
	// surface says "half a change landed" rather than inventing a fourth
	// category (§forgejo/config/identity-collapse,
	// §forgejo/observability/why-the-engine-is-off). One site per depth
	// the key can sit at, each making both assertions.
	t.Run("entry still declares the retired persona key", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			content string
		}{
			{"action entry", strings.Replace(validConfigYAML,
				"  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n    identity: implementer\n",
				"  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n    identity: implementer\n    persona: implementer\n", 1)},
			{"lens entry", strings.Replace(validConfigYAML,
				"  - identity: reviewer-security\n    prompt: \"/pr-security-reviewer {{.PRURL}}\"\n",
				"  - identity: reviewer-security\n    prompt: \"/pr-security-reviewer {{.PRURL}}\"\n    persona: reviewer\n", 1)},
			{"top level", strings.Replace(validConfigYAML,
				"org: example-org\n", "org: example-org\npersona: implementer\n", 1)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := LoadConfig(validateFixturePath(t, tc.content))
				if !errors.Is(err, ErrActionSetMismatch) {
					t.Fatalf("err = %v, want one wrapping ErrActionSetMismatch", err)
				}
				if !strings.Contains(err.Error(), "persona") {
					t.Errorf("error %q must name the retired key it refused", err)
				}
			})
		}
	})

	// A merge key is YAML's own way of including one mapping in another,
	// not a key the deployment invented, so the decode that refuses keys
	// beyond the schema must not refuse it: nine near-identical action
	// entries are exactly where an operator reaches for it
	// (§forgejo/config/validation).
	t.Run("a merge key includes another entry", func(t *testing.T) {
		body := strings.Replace(validConfigYAML,
			"  implement:\n", "  implement: &impl\n", 1)
		body = strings.Replace(body,
			"  reassess:\n    prompt: \"/issue-reassess {{.IssueURL}}\"\n    identity: implementer\n    timeout: 1h\n",
			"  reassess:\n    <<: *impl\n", 1)
		cfg, err := LoadConfig(validateFixturePath(t, body))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if got := cfg.Actions["reassess"]; got.Identity != "implementer" || got.Timeout != 2*time.Hour {
			t.Errorf("reassess = %+v, want the merged entry's identity and timeout", got)
		}
	})

	t.Run("any other validation failure", func(t *testing.T) {
		_, err := LoadConfig(validateFixturePath(t, strings.Replace(validConfigYAML, "reconcile_interval: 15m\n", "", 1)))
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		for _, sentinel := range []error{ErrConfigNotFound, ErrConfigUnreadable, ErrActionSetMismatch} {
			if errors.Is(err, sentinel) {
				t.Errorf("unrelated failure must not classify as %v: %v", sentinel, err)
			}
		}
	})
}

// validateFixturePath writes content into a fresh temp file and returns
// its path.
func validateFixturePath(t *testing.T, content string) string {
	t.Helper()
	return writeConfig(t, t.TempDir(), "forgejoeng.yaml", content)
}

// TestPromptFor looks up a prompt by action
// (§forgejo/config/canned-prompts), and pins that the retired review
// singletons are no longer configured actions — the lens list
// replaces them (§forgejo/config/review-lenses).
func TestPromptFor(t *testing.T) {
	cfg := loadValid(t)
	pc, err := cfg.PromptFor(ActionMergeOrWait)
	if err != nil {
		t.Fatalf("PromptFor(merge-or-wait): %v", err)
	}
	if pc.Identity != "reviewer" {
		t.Errorf("identity = %q", pc.Identity)
	}
	if _, err := cfg.PromptFor(ActionReview); err == nil {
		t.Error("PromptFor(review): expected error — review is configured through the lens list, not an action entry")
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
		Prompt:   "{{.Repo}}/{{.Kind}}/{{.Number}} {{.IssueURL}} {{.PRURL}} {{.Sender}} {{.Action}}",
		Identity: "p",
		Timeout:  time.Hour,
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
		ActionImplement, ActionReassess,
		ActionFixFeedback, ActionMergeOrWait, ActionRebase,
		ActionDecompose, ActionWrapUp, ActionTeardownForest, ActionSync,
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

// TestShippedSampleConfigIsValid pins the in-repo half of the coupling:
// Validate requires a prompt for every action the binary knows, so the
// sample config this repository ships must carry each one — a missing
// block is not a failed test but a silently disabled engine in
// `make test-ui` (§forgejo/config/validation, §forgejo/config/loading).
func TestShippedSampleConfigIsValid(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "config", "forgejoeng.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig(config/forgejoeng.yaml): %v", err)
	}
	if len(cfg.Actions) != len(AllActions) {
		t.Errorf("sample config carries %d actions, the binary knows %d", len(cfg.Actions), len(AllActions))
	}
	for _, a := range AllActions {
		if _, ok := cfg.Actions[string(a)]; !ok {
			t.Errorf("sample config has no %q block", a)
		}
	}
	// The lens list is the other half of the contract the binary
	// needs: a config without lenses loads no engine
	// (§forgejo/config/review-lenses).
	if len(cfg.Reviews) == 0 {
		t.Error("sample config carries no review lenses")
	}
}

// cannedPromptInvocation is the shape §forgejo/config/canned-prompts
// pins: a slash invocation, the URL placeholder as its argument, then
// any further arguments. A regexp rather than a Contains — a prompt that
// merely mentions the placeholder, before the command or with a URL of
// the wrong dispatch kind, must not pass.
var cannedPromptInvocation = regexp.MustCompile(`^/[a-z0-9-]+ \{\{\.(?:IssueURL|PRURL)\}\}(?P<args> .*)?$`)

// quotedSpan is one quoted argument of an invocation. Pi tokenises
// arguments on whitespace, respecting quotes only, so any further text
// has to sit inside a pair of them (§forgejo/config/canned-prompts).
var quotedSpan = regexp.MustCompile(`"[^"]*"`)

// TestShippedSampleConfigPromptsInvokeCannedPrompts pins the shape the
// canned prompts must keep: Pi expands a prompt template only when the
// dispatched message starts with "/<template-name>" (pi's prompt-template
// docs), so a prompt whose first byte is not "/" travels to the persona
// verbatim — the canned prompt is never invoked and its parameter value,
// the issue or pull request URL, never arrives. Dispatching a bare
// "decompose owner/repo issue 7" is how an outcome-decomposer run began
// with no issue URL at all. Every sample prompt therefore starts with a
// slash invocation and carries the URL its dispatch kind fills —
// .IssueURL for the issue-path actions, .PRURL for the PR-path ones
// (§forgejo/config/canned-prompts, §forgejo/decisions/actions).
//
// Any further text the config must state rides quoted: the template body
// renders only the placeholders it names, so an unquoted sentence arrives
// as loose positional arguments a template reading $1 alone discards.
func TestShippedSampleConfigPromptsInvokeCannedPrompts(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "config", "forgejoeng.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig(config/forgejoeng.yaml): %v", err)
	}

	// The URL placeholder each action's dispatch fills: the decision
	// table's issue-path rows are implement, reassess, decompose,
	// wrap-up, teardown-forest, and sync; the rest dispatch on a pull
	// request (§forgejo/decisions/transition-table).
	issuePath := map[Action]bool{
		ActionImplement:      true,
		ActionReassess:       true,
		ActionDecompose:      true,
		ActionWrapUp:         true,
		ActionTeardownForest: true,
		ActionSync:           true,
	}

	// The canned prompt each action invokes. The names are the
	// convention §forgejo/config/canned-prompts pins, so the sample, the
	// spec and the deployment stay on one page: when no template of that
	// name exists the message travels verbatim and nothing expands —
	// the failure #121 reported, and silent.
	command := map[Action]string{
		ActionImplement:      "issue-implementer",
		ActionReassess:       "issue-reassess",
		ActionFixFeedback:    "pr-feedback-fixer",
		ActionMergeOrWait:    "pr-merger",
		ActionRebase:         "pr-rebase",
		ActionDecompose:      "issue-decomposer",
		ActionWrapUp:         "issue-wrapper",
		ActionTeardownForest: "forest-teardown",
		ActionSync:           "set-lander",
	}

	for _, a := range AllActions {
		pc, ok := cfg.Actions[string(a)]
		if !ok {
			t.Fatalf("sample config has no %q block", a)
		}
		rest := cannedPromptInvocation.FindStringSubmatch(pc.Prompt)
		if rest == nil {
			t.Errorf("action %q: prompt %q is not a slash invocation with a URL placeholder as its argument — Pi would send it verbatim and the canned prompt would never be invoked", a, pc.Prompt)
			continue
		}
		want := "{{.PRURL}}"
		if issuePath[a] {
			want = "{{.IssueURL}}"
		}
		if !strings.Contains(pc.Prompt, want) {
			t.Errorf("action %q: prompt %q carries no %s — the dispatch would name no object for the agent to act on", a, pc.Prompt, want)
		}
		if head := "/" + command[a] + " "; !strings.HasPrefix(pc.Prompt, head) {
			t.Errorf("action %q: prompt %q does not invoke %s — the spec pins that name, and an unmatched command reaches the persona unexpanded", a, pc.Prompt, head)
		}
		// Text beyond the command and its URL argument must be quoted,
		// or it reaches the persona as loose positional arguments the
		// template may well never render (§forgejo/config/canned-prompts).
		if bare := strings.TrimSpace(quotedSpan.ReplaceAllString(rest[cannedPromptInvocation.SubexpIndex("args")], "")); bare != "" {
			t.Errorf("action %q: prompt %q passes %s unquoted — Pi splits arguments on whitespace, so the template would see a scatter of positionals instead of one sentence", a, pc.Prompt, bare)
		}
	}

	// The lens prompts are held to the same invocation shape, and
	// the three template names are the ones §forgejo/config/canned-prompts
	// pins now that /pr-reviewer is retired (§forgejo/config/review-lenses).
	lensCommands := map[string]string{
		"reviewer-minimalism":      "pr-minimalism-reviewer",
		"reviewer-maintainability": "pr-maintainability-reviewer",
		"reviewer-security":        "pr-security-reviewer",
	}
	if len(cfg.Reviews) != len(lensCommands) {
		t.Errorf("sample config carries %d lenses, the spec pins %d", len(cfg.Reviews), len(lensCommands))
	}
	for _, lens := range cfg.Reviews {
		want, ok := lensCommands[lens.Identity]
		if !ok {
			t.Errorf("lens %q: identity not pinned by the spec", lens.Identity)
			continue
		}
		rest := cannedPromptInvocation.FindStringSubmatch(lens.Prompt)
		if rest == nil {
			t.Errorf("lens %q: prompt %q is not a slash invocation with a URL placeholder as its argument", lens.Identity, lens.Prompt)
			continue
		}
		if !strings.Contains(lens.Prompt, "{{.PRURL}}") {
			t.Errorf("lens %q: prompt %q carries no {{.PRURL}} — the dispatch would name no object", lens.Identity, lens.Prompt)
		}
		if head := "/" + want + " "; !strings.HasPrefix(lens.Prompt, head) {
			t.Errorf("lens %q: prompt %q does not invoke %s — the spec pins that name", lens.Identity, lens.Prompt, head)
		}
	}

	// The decompose template is held to naming the fan-out cap
	// (§forgejo/config/canned-prompts): the agent must be told the
	// number and told to cite it when refusing — told, that is, in text
	// that survives the argument tokenisation.
	decompose := cfg.Actions[string(ActionDecompose)].Prompt
	if !strings.Contains(decompose, "20") || !strings.Contains(strings.ToLower(decompose), "cap") {
		t.Errorf("decompose prompt %q does not name the fan-out cap", decompose)
	}
	quoted := false
	for _, span := range quotedSpan.FindAllString(decompose, -1) {
		if strings.Contains(span, "20") && strings.Contains(strings.ToLower(span), "cap") {
			quoted = true
		}
	}
	if !quoted {
		t.Errorf("decompose prompt %q states the fan-out cap outside quotes, where Pi's argument splitting can drop it", decompose)
	}
}

// TestReviewLensesValid pins the accepted shape of the lens list: one
// declared identity, a prompt, and a positive timeout per lens, order
// preserved (§forgejo/config/review-lenses,
// §forgejo/config/identity-collapse, §forgejo/config/validation).
func TestReviewLensesValid(t *testing.T) {
	cfg := loadValid(t)
	if len(cfg.Reviews) != 3 {
		t.Fatalf("loaded %d lenses, want 3", len(cfg.Reviews))
	}
	if cfg.Reviews[0].Identity != "reviewer-minimalism" {
		t.Errorf("first lens = %q, want reviewer-minimalism — the list is ordered", cfg.Reviews[0].Identity)
	}
	if cfg.Reviews[0].Timeout != 30*time.Minute {
		t.Errorf("lens timeout = %s, want 30m", cfg.Reviews[0].Timeout)
	}
}

// TestReviewLensesRejected pins the load-time refusals: an empty list,
// a lens missing a field, a duplicate identity, an unparseable
// template or an unknown placeholder (§forgejo/config/validation).
func TestReviewLensesRejected(t *testing.T) {
	cases := []struct {
		name    string
		reviews string
		want    string
	}{
		{"empty", "reviews: []\n", "at least one"},
		{"missing identity", "reviews:\n  - prompt: \"/pr-review {{.PRURL}}\"\n    timeout: 30m\n", "identity is required"},
		{"duplicate identity", "reviews:\n  - identity: r\n    prompt: \"/a {{.PRURL}}\"\n    timeout: 30m\n  - identity: r\n    prompt: \"/b {{.PRURL}}\"\n    timeout: 30m\n", "duplicate lens identity"},
		{"missing prompt", "reviews:\n  - identity: r\n    timeout: 30m\n", "prompt is required"},
		{"zero timeout", "reviews:\n  - identity: r\n    prompt: \"/a {{.PRURL}}\"\n    timeout: 0s\n", "timeout must be positive"},
		{"bad template", "reviews:\n  - identity: r\n    prompt: \"/a {{.Nope}}\"\n    timeout: 30m\n", "reviews"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := validConfigYAML
			// Drop the fixture's own reviews block, append the case's.
			if i := strings.Index(body, "reviews:"); i >= 0 {
				body = body[:i]
			}
			path := validateFixturePath(t, body+tc.reviews)
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatalf("LoadConfig: expected error %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestReviewLensIdentityIsNoActorLogin pins the rule the single declared
// name puts on the load: a lens's login must not be a login the config
// also declares as an actor. One field is now both what a decision
// compares a review author against and the persona the task is
// materialised with, so a lens naming an actor would let that account
// satisfy a gate whose whole purpose is somebody else's verdict
// (§forgejo/config/review-lenses, §forgejo/config/identity-collapse).
func TestReviewLensIdentityIsNoActorLogin(t *testing.T) {
	// withoutReviews drops the fixture's lens list so a case can name
	// the one lens it is about.
	withoutReviews := func(y string) string {
		if i := strings.Index(y, "reviews:"); i >= 0 {
			return y[:i]
		}
		return y
	}
	lensNaming := func(login string) string {
		return "reviews:\n  - identity: " + login + "\n    prompt: \"/a {{.PRURL}}\"\n    timeout: 30m\n"
	}
	rename := func(y, key, login string) string {
		return strings.Replace(y, key+": "+login+"\n", key+": solo-actor\n", 1)
	}
	cases := []struct {
		name   string
		mutate func(string) string
	}{
		{"an action entry declares it", func(y string) string {
			// "reviewer" is declared by merge-or-wait and by neither
			// scalar key, so this case can only be fired by the loop
			// over the entries.
			return withoutReviews(y) + lensNaming("reviewer")
		}},
		{"implementer_identity declares it", func(y string) string {
			return withoutReviews(rename(y, "implementer_identity", "implementer")) + lensNaming("solo-actor")
		}},
		{"decomposer_identity declares it", func(y string) string {
			return withoutReviews(rename(y, "decomposer_identity", "decomposer")) + lensNaming("solo-actor")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := validateFixturePath(t, tc.mutate(validConfigYAML))
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), "also declared as an actor") {
				t.Fatalf("LoadConfig: want a refusal naming the actor collision, got %v", err)
			}
		})
	}
}

// TestRetiredReviewSingletonsRejected pins the config-or-binary
// mismatch when a deployment still names the retired singletons: the
// binary no longer dispatches them, so the config must stop naming
// them first (§forgejo/config/validation, §forgejo/config/review-lenses).
func TestRetiredReviewSingletonsRejected(t *testing.T) {
	for _, retired := range []string{"review", "re-review"} {
		t.Run(retired, func(t *testing.T) {
			body := strings.Replace(validConfigYAML,
				"  fix-feedback:",
				"  "+retired+":\n    prompt: \"/pr-reviewer {{.PRURL}}\"\n    identity: reviewer\n    timeout: 1h\n  fix-feedback:", 1)
			path := validateFixturePath(t, body)
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatalf("LoadConfig with retired %q action: expected error, got nil", retired)
			}
			if !errors.Is(err, ErrActionSetMismatch) {
				t.Errorf("error = %v, want ErrActionSetMismatch", err)
			}
		})
	}
}

// TestMissingReviewLensesIsMismatch pins the other direction: a binary
// that reviews through lenses refuses to start over a config that has
// not gained the list yet — the loud config-before-binary failure
// (§forgejo/config/validation).
func TestMissingReviewLensesIsMismatch(t *testing.T) {
	body := validConfigYAML
	if i := strings.Index(body, "reviews:"); i >= 0 {
		body = body[:i]
	}
	path := validateFixturePath(t, body)
	_, err := LoadConfig(path)
	if !errors.Is(err, ErrActionSetMismatch) {
		t.Errorf("error = %v, want ErrActionSetMismatch", err)
	}
}

// TestReviewLensesDirectoryMerge concatenates lenses across files in
// sorted order and refuses a lens identity defined twice
// (§forgejo/config/review-lenses, §forgejo/config/loading).
func TestReviewLensesDirectoryMerge(t *testing.T) {
	rest := validConfigYAML
	if j := strings.Index(rest, "reviews:"); j >= 0 {
		rest = rest[:j]
	}
	t.Run("concatenates in sorted order", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, "02-actions.yaml", rest)
		writeConfig(t, dir, "03-lenses-a.yaml", `
reviews:
  - identity: lens-one
    prompt: "/pr-review-one {{.PRURL}}"
    timeout: 30m
`)
		writeConfig(t, dir, "04-lenses-b.yaml", `
reviews:
  - identity: lens-two
    prompt: "/pr-review-two {{.PRURL}}"
    timeout: 30m
`)
		cfg, err := LoadConfig(dir)
		if err != nil {
			t.Fatalf("LoadConfig(dir): %v", err)
		}
		if len(cfg.Reviews) != 2 || cfg.Reviews[0].Identity != "lens-one" || cfg.Reviews[1].Identity != "lens-two" {
			t.Errorf("lenses = %+v, want lens-one then lens-two in file order", cfg.Reviews)
		}
	})
	t.Run("duplicate identity across files", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, "02-actions.yaml", rest)
		writeConfig(t, dir, "03-lenses-a.yaml", `
reviews:
  - identity: lens-one
    prompt: "/pr-review-one {{.PRURL}}"
    timeout: 30m
`)
		writeConfig(t, dir, "04-lenses-b.yaml", `
reviews:
  - identity: lens-one
    prompt: "/pr-review-two {{.PRURL}}"
    timeout: 30m
`)
		if _, err := LoadConfig(dir); err == nil || !strings.Contains(err.Error(), "already defined") {
			t.Fatalf("LoadConfig(dir): error = %v, want duplicate identity conflict", err)
		}
	})
}
