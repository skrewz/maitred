package forgejo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for the vocabulary that names why the engine is off
// (§forgejo/observability/why-the-engine-is-off): every construction
// path yields one typed cause and exactly one category.

// mapEnv adapts a table to Env for the tests that do not want to touch
// the real process environment.
func mapEnv(vars map[string]string) Env {
	return func(key string) (string, bool) {
		v, ok := vars[key]
		return v, ok
	}
}

// completeEnv is an environment with every credential set; the config
// path is supplied separately.
func completeEnv() map[string]string {
	return map[string]string{
		URLEnvVar:    "https://forgejo.example.com",
		TokenEnvVar:  "a-token",
		SecretEnvVar: "a-secret",
	}
}

// withEnvWithout drops the named keys from an environment table.
func withEnvWithout(vars map[string]string, keys ...string) map[string]string {
	out := make(map[string]string, len(vars))
	for k, v := range vars {
		drop := false
		for _, key := range keys {
			if k == key {
				drop = true
				break
			}
		}
		if !drop {
			out[k] = v
		}
	}
	return out
}

// TestCategoryString pins the text every surface prints
// (§forgejo/observability/why-the-engine-is-off).
func TestCategoryString(t *testing.T) {
	cases := []struct {
		category Category
		want     string
	}{
		{CategoryNotConfigured, "not configured"},
		{CategoryConfigUnreadable, "config unreadable"},
		{CategoryConfigBinaryMismatch, "config or binary mismatch"},
		{CategoryCredentialsMissing, "credentials missing"},
		{CategoryOther, "other fault"},
	}
	for _, tc := range cases {
		if got := tc.category.String(); got != tc.want {
			t.Errorf("Category(%d).String() = %q, want %q", tc.category, got, tc.want)
		}
	}
	if unknown := Category(99).String(); unknown == "" {
		t.Error("an unnamed category must still render non-empty text")
	}
}

// TestCategoryIsFault keeps "no configuration at all is not a fault"
// expressible on every surface (§forgejo/observability/why-the-engine-is-off).
func TestCategoryIsFault(t *testing.T) {
	if CategoryNotConfigured.IsFault() {
		t.Error("CategoryNotConfigured must not be a fault")
	}
	for _, c := range []Category{CategoryConfigUnreadable, CategoryConfigBinaryMismatch, CategoryCredentialsMissing, CategoryOther} {
		if !c.IsFault() {
			t.Errorf("%s must be a fault", c)
		}
	}
}

// TestCategoryOfIsTotal asserts the mapping is total: every error — and
// the absence of one — lands in exactly one category
// (§forgejo/observability/why-the-engine-is-off).
func TestCategoryOfIsTotal(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Category
	}{
		{"no error", nil, CategoryNotConfigured},
		{"no config", fmt.Errorf("stat config %q: %w", "/x", ErrConfigNotFound), CategoryNotConfigured},
		{"unreadable config", fmt.Errorf("read config %q: %w: permission denied", "/x", ErrConfigUnreadable), CategoryConfigUnreadable},
		{"missing action entry", fmt.Errorf("validate config %q: %w", "/x", fmt.Errorf("action %q: no canned prompt configured: %w", "sync", ErrActionSetMismatch)), CategoryConfigBinaryMismatch},
		{"unknown action entry", fmt.Errorf("validate config %q: %w", "/x", fmt.Errorf("unknown action %q: the decision function does not dispatch it: %w", "bogus", ErrActionSetMismatch)), CategoryConfigBinaryMismatch},
		{"incomplete environment", fmt.Errorf("%w: credentials", ErrEnvironmentIncomplete), CategoryCredentialsMissing},
		{"unclassified error", errors.New("something else"), CategoryOther},
		{"unclassified wrapped error", fmt.Errorf("outer: %w", errors.New("something else")), CategoryOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CategoryOf(tc.err); got != tc.want {
				t.Errorf("CategoryOf(%v) = %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}

// TestEngineStatusConstructionPaths classifies every way the engine can
// come up disabled, without constructing it
// (§forgejo/observability/why-the-engine-is-off).
func TestEngineStatusConstructionPaths(t *testing.T) {
	t.Run("no config at all", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "forgejoeng.yaml")
		cause, category := EngineStatus(path, mapEnv(completeEnv()))
		if !errors.Is(cause, ErrConfigNotFound) {
			t.Fatalf("cause = %v, want ErrConfigNotFound", cause)
		}
		if category != CategoryNotConfigured {
			t.Errorf("category = %s, want %s", category, CategoryNotConfigured)
		}
		if category.IsFault() {
			t.Error("no configuration at all is not a fault")
		}
	})

	t.Run("config present but unreadable", func(t *testing.T) {
		skipUnlessAccessDenied(t)
		path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", validConfigYAML)
		if err := os.Chmod(path, 0o000); err != nil {
			t.Skipf("chmod: %v", err)
		}
		cause, category := EngineStatus(path, mapEnv(completeEnv()))
		if !errors.Is(cause, ErrConfigUnreadable) {
			t.Fatalf("cause = %v, want ErrConfigUnreadable", cause)
		}
		if category != CategoryConfigUnreadable {
			t.Errorf("category = %s, want %s", category, CategoryConfigUnreadable)
		}
	})

	t.Run("config present but unparseable", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", "org: [unclosed")
		cause, category := EngineStatus(path, mapEnv(completeEnv()))
		if !errors.Is(cause, ErrConfigUnreadable) {
			t.Fatalf("cause = %v, want ErrConfigUnreadable", cause)
		}
		if category != CategoryConfigUnreadable {
			t.Errorf("category = %s, want %s", category, CategoryConfigUnreadable)
		}
	})

	t.Run("config directory unreadable", func(t *testing.T) {
		skipUnlessAccessDenied(t)
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Skipf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		cause, category := EngineStatus(dir, mapEnv(completeEnv()))
		if !errors.Is(cause, ErrConfigUnreadable) {
			t.Fatalf("cause = %v, want ErrConfigUnreadable", cause)
		}
		if category != CategoryConfigUnreadable {
			t.Errorf("category = %s, want %s", category, CategoryConfigUnreadable)
		}
	})

	t.Run("config omits an action the binary knows", func(t *testing.T) {
		yaml := strings.Replace(validConfigYAML,
			"  rebase:\n    prompt: \"/pr-rebase {{.PRURL}}\"\n    identity: implementer\n    timeout: 30m\n",
			"", 1)
		path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", yaml)
		cause, category := EngineStatus(path, mapEnv(completeEnv()))
		if !errors.Is(cause, ErrActionSetMismatch) {
			t.Fatalf("cause = %v, want ErrActionSetMismatch", cause)
		}
		if category != CategoryConfigBinaryMismatch {
			t.Errorf("category = %s, want %s", category, CategoryConfigBinaryMismatch)
		}
		if !strings.Contains(cause.Error(), "rebase") {
			t.Errorf("cause %q must name the action it failed on", cause)
		}
	})

	t.Run("config names an action the binary does not know", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", strings.Replace(validConfigYAML, "actions:", "actions:\n  bogus:\n    prompt: x\n    identity: p\n    timeout: 1h", 1))
		cause, category := EngineStatus(path, mapEnv(completeEnv()))
		if !errors.Is(cause, ErrActionSetMismatch) {
			t.Fatalf("cause = %v, want ErrActionSetMismatch", cause)
		}
		if category != CategoryConfigBinaryMismatch {
			t.Errorf("category = %s, want %s", category, CategoryConfigBinaryMismatch)
		}
	})

	t.Run("credentials missing", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			drop []string
		}{
			{"no url", []string{URLEnvVar}},
			{"no token", []string{TokenEnvVar}},
			{"no secret", []string{SecretEnvVar, SecretFileEnvVar}},
			{"empty secret", nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				vars := withEnvWithout(completeEnv(), tc.drop...)
				if tc.name == "empty secret" {
					vars[SecretEnvVar] = ""
				}
				path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", validConfigYAML)
				cause, category := EngineStatus(path, mapEnv(vars))
				if !errors.Is(cause, ErrEnvironmentIncomplete) {
					t.Fatalf("cause = %v, want ErrEnvironmentIncomplete", cause)
				}
				if category != CategoryCredentialsMissing {
					t.Errorf("category = %s, want %s", category, CategoryCredentialsMissing)
				}
				if !category.IsFault() {
					t.Error("a configured engine with no credentials is a fault")
				}
			})
		}
	})

	t.Run("mTLS pair set only halfway is a missing credential", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			cert string
			key  string
		}{
			{"cert without key", filepath.Join(t.TempDir(), "client.crt"), ""},
			{"key without cert", "", filepath.Join(t.TempDir(), "client.key")},
		} {
			t.Run(tc.name, func(t *testing.T) {
				vars := completeEnv()
				vars[ClientCertEnvVar] = tc.cert
				vars[ClientKeyEnvVar] = tc.key
				path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", validConfigYAML)
				cause, category := EngineStatus(path, mapEnv(vars))
				if !errors.Is(cause, ErrEnvironmentIncomplete) {
					t.Fatalf("cause = %v, want ErrEnvironmentIncomplete", cause)
				}
				if category != CategoryCredentialsMissing {
					t.Errorf("category = %s, want %s", category, CategoryCredentialsMissing)
				}
				if CategoryOf(cause) != category {
					t.Errorf("EngineStatus says %s, CategoryOf says %s: the two must agree", category, CategoryOf(cause))
				}
			})
		}
	})

	t.Run("mTLS pair named but unloadable is a fault", func(t *testing.T) {
		missing := t.TempDir()
		vars := completeEnv()
		vars[ClientCertEnvVar] = filepath.Join(missing, "client.crt")
		vars[ClientKeyEnvVar] = filepath.Join(missing, "client.key")
		path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", validConfigYAML)
		cause, category := EngineStatus(path, mapEnv(vars))
		if cause == nil {
			t.Fatal("cause = nil: startup fails on this pair, so a probe must not read healthy")
		}
		if errors.Is(cause, ErrEnvironmentIncomplete) {
			t.Errorf("a pair that will not load is not a missing credential: %v", cause)
		}
		if category != CategoryOther {
			t.Errorf("category = %s, want %s", category, CategoryOther)
		}
	})

	t.Run("mTLS pair set and loadable", func(t *testing.T) {
		certPath, keyPath := writeCertFiles(t, genTestCert(t, "maitred-client", false, nil))
		vars := completeEnv()
		vars[ClientCertEnvVar] = certPath
		vars[ClientKeyEnvVar] = keyPath
		path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", validConfigYAML)
		cause, category := EngineStatus(path, mapEnv(vars))
		if cause != nil {
			t.Fatalf("cause = %v, want nil", cause)
		}
		if category.IsFault() {
			t.Errorf("category = %s, want no fault", category)
		}
	})

	t.Run("config location holds no config", func(t *testing.T) {
		// A location the operator pointed the engine at that yields no
		// config is a fault, not the by-design absence of one
		// (§forgejo/observability/why-the-engine-is-off).
		dir := t.TempDir()
		cause, category := EngineStatus(dir, mapEnv(completeEnv()))
		if cause == nil {
			t.Fatal("expected a cause, got nil")
		}
		if errors.Is(cause, ErrConfigNotFound) {
			t.Errorf("an empty location is not the absence of one: %v", cause)
		}
		if category != CategoryOther {
			t.Errorf("category = %s, want %s", category, CategoryOther)
		}
	})

	t.Run("zero-byte config file", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", "")
		cause, category := EngineStatus(path, mapEnv(completeEnv()))
		if cause == nil {
			t.Fatal("expected a cause, got nil")
		}
		if category != CategoryOther {
			t.Errorf("category = %s, want %s", category, CategoryOther)
		}
	})

	t.Run("secret file set but unreadable", func(t *testing.T) {
		vars := withEnvWithout(completeEnv(), SecretEnvVar)
		vars[SecretFileEnvVar] = filepath.Join(t.TempDir(), "absent")
		path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", validConfigYAML)
		cause, category := EngineStatus(path, mapEnv(vars))
		if cause == nil {
			t.Fatal("expected a cause, got nil")
		}
		if errors.Is(cause, ErrEnvironmentIncomplete) {
			t.Errorf("an unreadable secret file is not a missing credential: %v", cause)
		}
		if category != CategoryOther {
			t.Errorf("category = %s, want %s", category, CategoryOther)
		}
	})

	t.Run("config valid but unrelated validation failure", func(t *testing.T) {
		yaml := strings.Replace(validConfigYAML, "reconcile_interval: 15m\n", "", 1)
		path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", yaml)
		cause, category := EngineStatus(path, mapEnv(completeEnv()))
		if cause == nil {
			t.Fatal("expected a cause, got nil")
		}
		if errors.Is(cause, ErrActionSetMismatch) || errors.Is(cause, ErrConfigUnreadable) || errors.Is(cause, ErrConfigNotFound) {
			t.Errorf("an unrelated validation failure must not classify as a named cause: %v", cause)
		}
		if category != CategoryOther {
			t.Errorf("category = %s, want %s", category, CategoryOther)
		}
	})

	t.Run("configured and sound", func(t *testing.T) {
		// The category here is the zero value, whose text is "not
		// configured": a surface with no cause names no category
		// (§forgejo/observability/why-the-engine-is-off).
		path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", validConfigYAML)
		cause, category := EngineStatus(path, mapEnv(completeEnv()))
		if cause != nil {
			t.Fatalf("cause = %v, want nil", cause)
		}
		if category.IsFault() {
			t.Errorf("category = %s, want no fault", category)
		}
	})

	t.Run("classification needs no engine", func(t *testing.T) {
		// The classifier takes only a path and an environment: no store
		// directory is created, no client built, no network touched
		// (§forgejo/observability/why-the-engine-is-off).
		dir := t.TempDir()
		path := filepath.Join(dir, "forgejoeng.yaml")
		cause, _ := EngineStatus(path, mapEnv(completeEnv()))
		if !errors.Is(cause, ErrConfigNotFound) {
			t.Fatalf("cause = %v, want ErrConfigNotFound", cause)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("EngineStatus created %d entries under %s", len(entries), dir)
		}
	})
}

// TestEngineStatusUsesTheGivenEnv proves the classifier reads the
// environment it is handed rather than the process's own
// (§forgejo/observability/why-the-engine-is-off).
func TestEngineStatusUsesTheGivenEnv(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "forgejoeng.yaml", validConfigYAML)
	t.Setenv(URLEnvVar, "https://forgejo.example.com")
	t.Setenv(TokenEnvVar, "a-token")
	t.Setenv(SecretEnvVar, "a-secret")
	t.Setenv(ClientCertEnvVar, "")
	t.Setenv(ClientKeyEnvVar, "")
	if _, category := EngineStatus(path, mapEnv(nil)); category != CategoryCredentialsMissing {
		t.Errorf("category = %s, want %s from the injected (empty) environment", category, CategoryCredentialsMissing)
	}
	if cause, category := EngineStatus(path, ProcessEnv()); cause != nil {
		t.Errorf("ProcessEnv: cause = %v, category = %s, want the process environment to read complete", cause, category)
	}
	// A nil Env reads the process environment, the way LoadSecret does.
	if cause, category := EngineStatus(path, nil); cause != nil {
		t.Errorf("nil Env: cause = %v, category = %s, want the process environment to read complete", cause, category)
	}
}

// TestCheckEnvironment names every variable that is missing
// (§forgejo/webhook/configuration).
func TestCheckEnvironment(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		token  string
		secret string
		wantOK bool
	}{
		{"all present", "https://forgejo.example.com", "a-token", "a-secret", true},
		{"no url", "", "a-token", "a-secret", false},
		{"no token", "https://forgejo.example.com", "", "a-secret", false},
		{"no secret", "https://forgejo.example.com", "a-token", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckEnvironment(tc.url, tc.token, tc.secret)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("CheckEnvironment: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrEnvironmentIncomplete) {
				t.Fatalf("err = %v, want ErrEnvironmentIncomplete", err)
			}
			for _, name := range []string{URLEnvVar, TokenEnvVar, SecretFileEnvVar, SecretEnvVar} {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("error %q must name %s", err, name)
				}
			}
		})
	}
}

// TestEngineStateWord pins the one-word engine state a surface that
// carries no cause text renders
// (§forgejo/observability/the-dashboard-view,
// §forgejo/observability/why-the-engine-is-off): an engine that
// is up is named with the up word and no category, an engine that is
// off with a cause is named by its category alone, and the nil-engine
// states — never configured versus a failed construction — stay
// distinguishable.
func TestEngineStateWord(t *testing.T) {
	cases := []struct {
		name  string
		up    bool
		cause error
		want  string
	}{
		{"up", true, nil, EngineUpWord},
		{"off, nothing configured", false, fmt.Errorf("stat config %q: %w", "/x", ErrConfigNotFound), CategoryNotConfigured.String()},
		{"off, no cause in hand", false, nil, CategoryNotConfigured.String()},
		{"off, unreadable config", false, fmt.Errorf("read config: %w", ErrConfigUnreadable), CategoryConfigUnreadable.String()},
		{"off, half a landing set", false, fmt.Errorf("validate config: %w", ErrActionSetMismatch), CategoryConfigBinaryMismatch.String()},
		{"off, credentials missing", false, fmt.Errorf("new engine: %w", ErrEnvironmentIncomplete), CategoryCredentialsMissing.String()},
		{"off, unnamed fault", false, errors.New("mkdir /nonexistent: boom"), CategoryOther.String()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EngineStateWord(tc.up, tc.cause)
			if got != tc.want {
				t.Errorf("EngineStateWord(%t, %v) = %q, want %q", tc.up, tc.cause, got, tc.want)
			}
			if tc.up && got == CategoryNotConfigured.String() {
				t.Errorf("a running engine must not be named by the zero-value category: %q", got)
			}
		})
	}
}

// TestNonFaultWords pins the words a surface must not read as a fault:
// the up word and the not-configured word, exactly
// (§forgejo/observability/why-the-engine-is-off). The dashboard's
// Forgejo view compares the state it is served against those two words
// in JavaScript, in a file no Go test executes, so the words can only
// move together with this test. A reworded category, or a new state
// that is not a fault, has to fail here before it can quietly make the
// dashboard call a healthy engine a fault — or hide a real one.
func TestNonFaultWords(t *testing.T) {
	want := map[string]bool{EngineUpWord: true, CategoryNotConfigured.String(): true}
	if EngineUpWord == CategoryNotConfigured.String() {
		t.Fatalf("the up word and the not-configured word are both %q; a surface cannot tell them apart", EngineUpWord)
	}

	got := map[string]bool{EngineUpWord: true}
	for _, c := range []Category{CategoryNotConfigured, CategoryConfigUnreadable, CategoryConfigBinaryMismatch, CategoryCredentialsMissing, CategoryOther} {
		if !c.IsFault() {
			got[c.String()] = true
		}
	}

	if len(got) != len(want) {
		t.Errorf("non-fault words = %d words %v, want %d words %v — the fault test in pkg/web/static/index.html must move with it", len(got), got, len(want), want)
	}
	for word := range want {
		if !got[word] {
			t.Errorf("non-fault word %q is missing from the enum's words %v", word, got)
		}
	}
	for word := range got {
		if !want[word] {
			t.Errorf("word %q is not a fault in Go but is not one of the words the dashboard knows (%v)", word, want)
		}
	}
}
