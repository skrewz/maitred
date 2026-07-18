// Package trigger provides the core types and configuration loading for
// maitred's periodic trigger engine. Triggers are defined in YAML files
// under a .d/ directory, parsed into TriggerDefinition structs, and
// executed by the engine according to their schedule.
package trigger

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"text/template"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

// TriggerType represents the kind of trigger.
type TriggerType string

const (
	TypePeriodic TriggerType = "periodic"
)

// ParseSchedule validates and returns a schedule string.
// Supports Go durations (@every 1h), cron expressions (0 */6 * * *),
// and the special @webhook sentinel for triggers that are only dispatched
// via the webhook API (not scheduled by the engine).
func ParseSchedule(s string) (string, error) {
	// Webhook sentinel — triggers with this schedule are only fired
	// via the webhook API, never by the periodic engine.
	if s == "@webhook" {
		return s, nil
	}

	// Check if it's a duration-based schedule
	if len(s) >= 8 && s[:8] == "@every " {
		dur, err := time.ParseDuration(s[7:])
		if err != nil {
			return "", fmt.Errorf("invalid duration in schedule %q: %w", s, err)
		}
		if dur <= 0 {
			return "", fmt.Errorf("schedule duration must be positive: %s", s)
		}
		return s, nil
	}

	// Validate as cron expression
	_, err := cron.ParseStandard(s)
	if err != nil {
		return "", fmt.Errorf("invalid cron expression %q: %w", s, err)
	}
	return s, nil
}

// triggerConfig is the raw YAML structure for a single trigger file.
type triggerConfig struct {
	Triggers []triggerFileEntry `yaml:"triggers"`
}

// triggerFileEntry is a single trigger definition in YAML.
type triggerFileEntry struct {
	ID       string      `yaml:"id"`
	Type     TriggerType `yaml:"type"`
	Schedule string      `yaml:"schedule"`
	Prompt   string      `yaml:"prompt"`
	Tags     []string    `yaml:"tags,omitempty"`
	Timeout  int         `yaml:"timeout,omitempty"`
}

// TriggerDefinition is the parsed, validated form of a trigger.
type TriggerDefinition struct {
	ID       string      `yaml:"id" json:"id"`
	Type     TriggerType `yaml:"type" json:"type"`
	Schedule string      `yaml:"schedule" json:"schedule"`
	Prompt   string      `yaml:"prompt" json:"prompt"`
	Tags     []string    `yaml:"tags,omitempty" json:"tags,omitempty"`
	Timeout  int         `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// EvalPromptTemplate evaluates the trigger's prompt template with the
// given lastRun time. The template has access to .LastRun (RFC3339 format).
func (d *TriggerDefinition) EvalPromptTemplate(lastRun time.Time) (string, error) {
	return d.EvalPromptTemplateWith(nil, lastRun)
}

// templatePayload recursively converts map[string]interface{} values
// into a form that Go templates can traverse with dot notation.
func templatePayload(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, v := range val {
			result[k] = templatePayload(v)
		}
		return result
	case map[interface{}]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, v := range val {
			if ks, ok := k.(string); ok {
				result[ks] = templatePayload(v)
			}
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, v := range val {
			result[i] = templatePayload(v)
		}
		return result
	default:
		return v
	}
}

// EvalPromptTemplateWith evaluates the trigger's prompt template with
// the given payload and lastRun time. The template has access to:
//   - .LastRun (RFC3339 format of lastRun)
//   - .Payload (the payload map, if non-nil)
func (d *TriggerDefinition) EvalPromptTemplateWith(payload map[string]interface{}, lastRun time.Time) (string, error) {
	funcs := template.FuncMap{
		"TrimSuffix": func(s, suffix string) string {
			if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
				return s[:len(s)-len(suffix)]
			}
			return s
		},
		"index": func(m map[string]interface{}, key string) interface{} {
			if m == nil {
				return nil
			}
			return m[key]
		},
	}

	tmpl, err := template.New("prompt").Funcs(funcs).Parse(d.Prompt)
	if err != nil {
		return "", fmt.Errorf("trigger %s: invalid prompt template: %w", d.ID, err)
	}

	ctx := map[string]interface{}{
		"LastRun": lastRun.Format(time.RFC3339),
	}
	if payload != nil {
		ctx["Payload"] = templatePayload(payload)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("trigger %s: failed to execute prompt template: %w", d.ID, err)
	}

	return buf.String(), nil
}

// LoadTriggerDefinitions reads all .yaml and .yml files from the given
// directory and all its subdirectories, returning all parsed
// TriggerDefinitions. Files are processed in sorted order by full path,
// enabling deterministic loading across nested configurations.
//
// The directory structure follows the .d/ convention: any YAML file in the
// directory tree is loaded and merged. This allows splitting trigger configs
// across multiple files (e.g., 01-base.yaml, 02-models.yaml) and organizing
// them into subdirectories (e.g., triggers.d/periodic/, triggers.d/webhook/).
func LoadTriggerDefinitions(dir string) ([]TriggerDefinition, error) {
	type fileEntry struct {
		path string
		defs []TriggerDefinition
	}

	var files []fileEntry

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if path == dir {
			return nil
		}

		if d.IsDir() {
			return nil
		}

		name := d.Name()
		lower := filepath.Ext(name)
		if lower != ".yaml" && lower != ".yml" {
			return nil
		}

		defs, err := loadTriggerFile(path)
		if err != nil {
			return fmt.Errorf("load %s: %w", path, err)
		}
		files = append(files, fileEntry{path: path, defs: defs})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk trigger directory %q: %w", dir, err)
	}

	// Sort files by path for deterministic ordering
	sort.Slice(files, func(i, j int) bool {
		return files[i].path < files[j].path
	})

	allDefs := make([]TriggerDefinition, 0)
	for _, f := range files {
		allDefs = append(allDefs, f.defs...)
	}

	return allDefs, nil
}

// loadTriggerFile parses a single YAML trigger file.
func loadTriggerFile(path string) ([]TriggerDefinition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	var cfg triggerConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}

	var defs []TriggerDefinition
	for _, entry := range cfg.Triggers {
		def := TriggerDefinition{
			ID:       entry.ID,
			Type:     entry.Type,
			Schedule: entry.Schedule,
			Prompt:   entry.Prompt,
			Tags:     entry.Tags,
			Timeout:  entry.Timeout,
		}

		// Validate
		if def.ID == "" {
			return nil, fmt.Errorf("trigger %s: id is required", path)
		}
		if def.Type != TypePeriodic {
			return nil, fmt.Errorf("trigger %s: unsupported type %q", def.ID, def.Type)
		}
		if def.Schedule == "" {
			return nil, fmt.Errorf("trigger %s: schedule is required", def.ID)
		}
		if def.Prompt == "" {
			return nil, fmt.Errorf("trigger %s: prompt is required", def.ID)
		}
		if _, err := ParseSchedule(def.Schedule); err != nil {
			return nil, fmt.Errorf("trigger %s: %w", def.ID, err)
		}

		defs = append(defs, def)
	}

	return defs, nil
}
