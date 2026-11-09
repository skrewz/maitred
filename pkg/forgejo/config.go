package forgejo

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
)

// AllActions lists every action the decision function references
// (§forgejo/config/validation): the config must cover each of them.
// The retired `review` and `re-review` singletons are absent: reviews
// are configured as the ordered lens list below, and a config that
// still names either singleton fails the unknown-action check — the
// loud half of the config-before-binary change
// (§forgejo/config/review-lenses, §forgejo/config/validation).
var AllActions = []Action{
	ActionImplement,
	ActionReassess,
	ActionFixFeedback,
	ActionMergeOrWait,
	ActionRebase,
	ActionDecompose,
	ActionWrapUp,
	ActionTeardownForest,
	ActionSync,
}

// ConfigEnvVar is the environment variable that locates the engine
// config (§forgejo/config/loading).
const ConfigEnvVar = "MAITRED_FORGEJOENG_DIR"

// DefaultConfigPath is the engine config location when ConfigEnvVar is
// unset (§forgejo/config/loading).
const DefaultConfigPath = "/etc/maitred/forgejoeng.yaml"

// ReviewLens is one configured review lens: an identity whose approval
// the gates count, with the canned prompt and persona that dispatch a
// review from it (§forgejo/config/review-lenses).
type ReviewLens struct {
	// Identity is the Forgejo login the lens reviews as; the decision
	// function compares it against review authors to decide which
	// lenses still owe a verdict (§forgejo/decisions/review-lenses).
	Identity string `yaml:"identity"`
	// Prompt is the canned prompt template, the hotelier task
	// invocation, shaped exactly like an action's (§forgejo/config/canned-prompts).
	Prompt string `yaml:"prompt"`
	// Persona is the persona the lens task runs as.
	Persona string `yaml:"persona"`
	// Timeout is how long the lens task may run.
	Timeout time.Duration `yaml:"timeout"`
}

// PromptConfig is the canned prompt for one action
// (§forgejo/config/canned-prompts).
type PromptConfig struct {
	// Prompt is the canned prompt template: the hotelier task invocation
	// plus its pre-flight checks.
	Prompt string `yaml:"prompt"`
	// Persona is the persona the task runs as.
	Persona string `yaml:"persona"`
	// Timeout is how long the task may run.
	Timeout time.Duration `yaml:"timeout"`
}

// Config is the engine's configuration: canned prompts plus the scalar
// keys (§forgejo/config/canned-prompts, §forgejo/config/decomposer-identity,
// §forgejo/config/implementer-identity).
type Config struct {
	// Org is the organisation whose role-holding repositories the
	// engine tracks (§forgejo/webhook/repository-roles).
	Org string `yaml:"org"`
	// ReconcileInterval is how often the reconciliation sweep runs
	// (§forgejo/reconciliation/scheduling).
	ReconcileInterval time.Duration `yaml:"reconcile_interval"`
	// DecomposerIdentity names the Forgejo identity that performs
	// decomposition — the author of forest members
	// (§forgejo/config/decomposer-identity). Optional: retained as the
	// operator's declaration of who composes forests; read by nothing in
	// the engine beyond load and merge, never as a decision gate — the
	// forest-seeding label gates a member, not its author
	// (s-git/maitred#135). Unset is never a load error.
	DecomposerIdentity string `yaml:"decomposer_identity"`
	// ImplementerIdentity names the Forgejo identity that implements
	// work and fixes review feedback (§forgejo/config/implementer-identity).
	// Optional: when set, the fix-feedback ownership gate holds the
	// action off on PRs authored by anyone else; when unset the gate
	// is inert and fix-feedback dispatches as before
	// (§forgejo/decisions/loop-prevention, s-git/maitred#143). Unset is
	// never a load error.
	ImplementerIdentity string `yaml:"implementer_identity"`
	// Actions maps each action the decision function references to its
	// canned prompt.
	Actions map[string]PromptConfig `yaml:"actions"`
	// Reviews is the ordered list of review lenses (§forgejo/config/review-lenses).
	// It replaces the retired `review` and `re-review` singletons;
	// extending or contracting the chain is a config edit. At least one
	// lens is required (§forgejo/config/validation).
	Reviews []ReviewLens `yaml:"reviews"`
}

// Placeholders are the values the engine fills into a prompt template at
// dispatch time, from the event and the re-fetched state
// (§forgejo/config/placeholders).
type Placeholders struct {
	// Repo is the affected repository's "owner/repo" full name.
	Repo string
	// Kind is "issue" or "pr".
	Kind string
	// Number is the issue or pull request number.
	Number int
	// IssueURL is the issue's html_url (empty for PR dispatches).
	IssueURL string
	// PRURL is the pull request's html_url (empty for issue dispatches).
	PRURL string
	// Sender is the login of the user who caused the event.
	Sender string
	// Action is the dispatched action's name.
	Action string
}

// Render fills the prompt template's placeholders and returns the canned
// prompt (§forgejo/config/placeholders).
func (pc *PromptConfig) Render(p Placeholders) (string, error) {
	tmpl, err := template.New("prompt").Parse(pc.Prompt)
	if err != nil {
		return "", fmt.Errorf("invalid prompt template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, p); err != nil {
		return "", fmt.Errorf("invalid prompt template: %w", err)
	}
	return buf.String(), nil
}

// PromptFor returns the canned prompt config for action
// (§forgejo/config/canned-prompts).
func (c *Config) PromptFor(a Action) (*PromptConfig, error) {
	pc, ok := c.Actions[string(a)]
	if !ok {
		return nil, fmt.Errorf("no canned prompt configured for action %q", a)
	}
	return &pc, nil
}

// Validate checks the config at load time — mirroring the trigger
// Validate(): a missing prompt, an unparseable template, or an unknown
// placeholder is a load error, never a runtime surprise
// (§forgejo/config/validation).
func (c *Config) Validate() error {
	if c.Org == "" {
		return fmt.Errorf("org is required")
	}
	if c.ReconcileInterval <= 0 {
		return fmt.Errorf("reconcile_interval must be positive")
	}
	for _, a := range AllActions {
		pc, ok := c.Actions[string(a)]
		if !ok {
			return fmt.Errorf("action %q: no canned prompt configured: %w", a, ErrActionSetMismatch)
		}
		if pc.Prompt == "" {
			return fmt.Errorf("action %q: prompt is required", a)
		}
		if pc.Persona == "" {
			return fmt.Errorf("action %q: persona is required", a)
		}
		if pc.Timeout <= 0 {
			return fmt.Errorf("action %q: timeout must be positive", a)
		}
		// Execute against a zero Placeholders: this catches templates
		// that do not parse and placeholders that are not in the
		// placeholder table (a struct field reference that does not
		// exist fails at execution).
		if _, err := pc.Render(Placeholders{}); err != nil {
			return fmt.Errorf("action %q: %w", a, err)
		}
	}
	for name := range c.Actions {
		if !isKnownAction(name) {
			return fmt.Errorf("unknown action %q: the decision function does not dispatch it: %w", name, ErrActionSetMismatch)
		}
	}
	if err := c.validateReviewLenses(); err != nil {
		return err
	}
	return nil
}

// validateReviewLenses checks the lens list: at least one lens, each
// lens complete (identity, prompt, persona, positive timeout), each
// lens's prompt parseable with only known placeholders, and no
// duplicate identity (§forgejo/config/review-lenses,
// §forgejo/config/validation).
func (c *Config) validateReviewLenses() error {
	if len(c.Reviews) == 0 {
		return fmt.Errorf("reviews: at least one review lens is required: %w", ErrActionSetMismatch)
	}
	seen := map[string]bool{}
	for i, lens := range c.Reviews {
		if lens.Identity == "" {
			return fmt.Errorf("reviews[%d]: identity is required", i)
		}
		if seen[lens.Identity] {
			return fmt.Errorf("reviews: duplicate lens identity %q", lens.Identity)
		}
		seen[lens.Identity] = true
		if lens.Prompt == "" {
			return fmt.Errorf("reviews %q: prompt is required", lens.Identity)
		}
		if lens.Persona == "" {
			return fmt.Errorf("reviews %q: persona is required", lens.Identity)
		}
		if lens.Timeout <= 0 {
			return fmt.Errorf("reviews %q: timeout must be positive", lens.Identity)
		}
		if _, err := (&PromptConfig{Prompt: lens.Prompt}).Render(Placeholders{}); err != nil {
			return fmt.Errorf("reviews %q: %w", lens.Identity, err)
		}
	}
	return nil
}

// isKnownAction reports whether name is one of the decision function's
// actions (§forgejo/config/validation).
func isKnownAction(name string) bool {
	for _, a := range AllActions {
		if string(a) == name {
			return true
		}
	}
	return false
}

// The load-time causes, typed so a surface can name which one it is
// looking at instead of re-reading a sentence (§forgejo/observability/why-the-engine-is-off).
var (
	// ErrConfigNotFound means no engine config exists at the given
	// location. It wraps fs.ErrNotExist so errors.Is matches either
	// sentinel, and it is the one definition of "nothing is configured
	// here" (§forgejo/config/loading).
	ErrConfigNotFound = fmt.Errorf("engine config not found: %w", fs.ErrNotExist)
	// ErrConfigUnreadable means something is at the location but the
	// engine cannot make a config out of it: a path it cannot stat,
	// read, or parse (§forgejo/config/loading).
	ErrConfigUnreadable = errors.New("config unreadable")
	// ErrActionSetMismatch means the config and the binary disagree
	// about the action set, in either direction — the signature of half
	// a config-and-binary change having landed
	// (§forgejo/config/validation).
	ErrActionSetMismatch = errors.New("config and binary disagree")
)

// LoadConfig loads the engine config from path — a single file, or a
// directory of .yaml/.yml files merged in sorted order — and validates
// it (§forgejo/config/loading, §forgejo/config/validation). Every way it
// fails is recognisable with errors.Is: ErrConfigNotFound when nothing
// is at path, ErrConfigUnreadable when the path or its contents cannot
// be read, ErrActionSetMismatch when the config and the binary disagree
// about the action set.
func LoadConfig(path string) (*Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("stat config %q: %w", path, ErrConfigNotFound)
		}
		return nil, fmt.Errorf("stat config %q: %w: %w", path, ErrConfigUnreadable, err)
	}

	var cfg *Config
	if info.IsDir() {
		cfg, err = loadConfigDir(path)
	} else {
		cfg, err = loadConfigFile(path)
	}
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %q: %w", path, err)
	}
	return cfg, nil
}

// loadConfigFile parses a single config file.
func loadConfigFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w: %w", path, ErrConfigUnreadable, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w: %w", path, ErrConfigUnreadable, err)
	}
	return &cfg, nil
}

// loadConfigDir parses every .yaml/.yml file in dir, in sorted order,
// and merges them: the org, the optional decomposer identity, and the
// optional implementer identity must agree, and an action may be
// defined in only one file (§forgejo/config/loading,
// §forgejo/config/validation, §forgejo/config/decomposer-identity,
// §forgejo/config/implementer-identity).
func loadConfigDir(dir string) (*Config, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read config directory %q: %w: %w", dir, ErrConfigUnreadable, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			files = append(files, name)
		}
	}
	sort.Strings(files)

	merged := &Config{}
	for _, name := range files {
		path := filepath.Join(dir, name)
		cfg, err := loadConfigFile(path)
		if err != nil {
			return nil, err
		}
		if cfg.Org != "" && merged.Org != "" && cfg.Org != merged.Org {
			return nil, fmt.Errorf("config %q: org %q conflicts with %q", name, cfg.Org, merged.Org)
		}
		if cfg.Org != "" {
			merged.Org = cfg.Org
		}
		if cfg.ReconcileInterval > 0 && merged.ReconcileInterval > 0 && cfg.ReconcileInterval != merged.ReconcileInterval {
			return nil, fmt.Errorf("config %q: reconcile_interval %s conflicts with %s", name, cfg.ReconcileInterval, merged.ReconcileInterval)
		}
		if cfg.ReconcileInterval > 0 {
			merged.ReconcileInterval = cfg.ReconcileInterval
		}
		if cfg.DecomposerIdentity != "" && merged.DecomposerIdentity != "" && cfg.DecomposerIdentity != merged.DecomposerIdentity {
			return nil, fmt.Errorf("config %q: decomposer_identity %q conflicts with %q", name, cfg.DecomposerIdentity, merged.DecomposerIdentity)
		}
		if cfg.DecomposerIdentity != "" {
			merged.DecomposerIdentity = cfg.DecomposerIdentity
		}
		if cfg.ImplementerIdentity != "" && merged.ImplementerIdentity != "" && cfg.ImplementerIdentity != merged.ImplementerIdentity {
			return nil, fmt.Errorf("config %q: implementer_identity %q conflicts with %q", name, cfg.ImplementerIdentity, merged.ImplementerIdentity)
		}
		if cfg.ImplementerIdentity != "" {
			merged.ImplementerIdentity = cfg.ImplementerIdentity
		}
		if merged.Actions == nil {
			merged.Actions = make(map[string]PromptConfig)
		}
		for action, pc := range cfg.Actions {
			if _, ok := merged.Actions[action]; ok {
				return nil, fmt.Errorf("config %q: action %q is already defined", name, action)
			}
			merged.Actions[action] = pc
		}
		// Lenses concatenate in sorted file order, so the chain's
		// order stays a config fact; a lens identity defined twice is
		// the same conflict the scalar keys refuse
		// (§forgejo/config/review-lenses). The non-empty requirement
		// belongs to Validate, not the merge: one file of a directory
		// may lawfully hold no lenses.
		for _, lens := range cfg.Reviews {
			for _, have := range merged.Reviews {
				if lens.Identity != "" && lens.Identity == have.Identity {
					return nil, fmt.Errorf("config %q: review lens identity %q is already defined", name, lens.Identity)
				}
			}
			merged.Reviews = append(merged.Reviews, lens)
		}
	}
	return merged, nil
}
