package forgejo

import (
	"bytes"
	"fmt"
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
var AllActions = []Action{
	ActionImplement,
	ActionReassess,
	ActionReview,
	ActionReReview,
	ActionFixFeedback,
	ActionMergeOrWait,
	ActionRebase,
}

// ConfigEnvVar is the environment variable that locates the engine
// config (§forgejo/config/loading).
const ConfigEnvVar = "MAITRED_FORGEJOENG_DIR"

// DefaultConfigPath is the engine config location when ConfigEnvVar is
// unset (§forgejo/config/loading).
const DefaultConfigPath = "/etc/maitred/forgejoeng.yaml"

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

// Config is the engine's canned-prompt configuration
// (§forgejo/config/canned-prompts).
type Config struct {
	// Org is the organisation whose maitred-enabled repositories the
	// engine tracks.
	Org string `yaml:"org"`
	// Actions maps each action the decision function references to its
	// canned prompt.
	Actions map[string]PromptConfig `yaml:"actions"`
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
	for _, a := range AllActions {
		pc, ok := c.Actions[string(a)]
		if !ok {
			return fmt.Errorf("action %q: no canned prompt configured", a)
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
			return fmt.Errorf("unknown action %q: the decision function does not dispatch it", name)
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

// LoadConfig loads the engine config from path — a single file, or a
// directory of .yaml/.yml files merged in sorted order — and validates
// it (§forgejo/config/loading, §forgejo/config/validation).
func LoadConfig(path string) (*Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat config %q: %w", path, err)
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
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	return &cfg, nil
}

// loadConfigDir parses every .yaml/.yml file in dir, in sorted order,
// and merges them: the org must agree, and an action may be defined in
// only one file (§forgejo/config/loading, §forgejo/config/validation).
func loadConfigDir(dir string) (*Config, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read config directory %q: %w", dir, err)
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
		if merged.Actions == nil {
			merged.Actions = make(map[string]PromptConfig)
		}
		for action, pc := range cfg.Actions {
			if _, ok := merged.Actions[action]; ok {
				return nil, fmt.Errorf("config %q: action %q is already defined", name, action)
			}
			merged.Actions[action] = pc
		}
	}
	return merged, nil
}
