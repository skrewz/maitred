package forgejo

import (
	"errors"
	"fmt"
	"os"
)

// Category is why the Forgejo engine is off, in the words the surfaces
// print — the disabled route's 503 body, `maitred --health`, and the
// dashboard's Forgejo view all name the same cause with it
// (§forgejo/observability/why-the-engine-is-off). The four fault values
// are the ones an operator asked for; the fifth,
// CategoryNotConfigured, is what keeps "no configuration at all is not
// a fault" expressible on every surface.
type Category int

const (
	// CategoryNotConfigured is the engine disabled by design: nothing
	// is configured at the configured location. Not a fault.
	CategoryNotConfigured Category = iota
	// CategoryConfigUnreadable is a config that is present but cannot
	// be read or parsed.
	CategoryConfigUnreadable
	// CategoryConfigBinaryMismatch is a config and a binary that
	// disagree about the action set — half a landing set deployed
	// (§forgejo/config/validation).
	CategoryConfigBinaryMismatch
	// CategoryCredentialsMissing is a config that enables the engine
	// while the environment cannot serve it.
	CategoryCredentialsMissing
	// CategoryOther is any fault the named causes do not describe.
	CategoryOther
)

// String is the text every surface prints (§forgejo/observability/why-the-engine-is-off).
func (c Category) String() string {
	switch c {
	case CategoryNotConfigured:
		return "not configured"
	case CategoryConfigUnreadable:
		return "config unreadable"
	case CategoryConfigBinaryMismatch:
		return "config or binary mismatch"
	case CategoryCredentialsMissing:
		return "credentials missing"
	case CategoryOther:
		return "other fault"
	default:
		return "unclassified fault"
	}
}

// IsFault separates a fault from the by-design absence of
// configuration, which is the distinction every surface has to make
// (§forgejo/observability/why-the-engine-is-off).
func (c Category) IsFault() bool { return c != CategoryNotConfigured }

// The environment variables that enable the engine
// (§forgejo/webhook/configuration). They are named here so the startup
// path and the classifier read the same set.
const (
	// URLEnvVar names the Forgejo instance to talk to.
	URLEnvVar = "MAITRED_FORGEJO_URL"
	// TokenEnvVar carries the API token.
	TokenEnvVar = "MAITRED_FORGEJO_TOKEN"
)

// ErrEnvironmentIncomplete is returned when the engine config enables
// the engine but the environment cannot serve it: the instance URL, the
// token, or the shared secret is missing
// (§forgejo/webhook/configuration). It classifies as
// CategoryCredentialsMissing.
var ErrEnvironmentIncomplete = errors.New("environment incomplete")

// Env is an environment lookup: the value of key and whether it is set.
// os.LookupEnv satisfies it; a caller may hand over a table instead, so
// that a health probe reads the environment without the process having
// to be the one that starts the engine (§forgejo/observability/why-the-engine-is-off).
type Env func(key string) (string, bool)

// ProcessEnv is the environment of the running process.
func ProcessEnv() Env { return os.LookupEnv }

// lookup reads one variable from an Env; a nil Env reads the process's
// own environment.
func lookup(env Env, key string) string {
	if env == nil {
		return os.Getenv(key)
	}
	v, _ := env(key)
	return v
}

// CheckEnvironment reports whether the credentials an engine the config
// has enabled can actually serve with: the instance URL, the API token,
// and the shared secret (§forgejo/webhook/configuration). The error
// wraps ErrEnvironmentIncomplete and names every variable the operator
// can set, so the 503 body and the startup log point at the fix.
func CheckEnvironment(url, token, secret string) error {
	if url != "" && token != "" && secret != "" {
		return nil
	}
	return fmt.Errorf("%w: %s, %s and the shared secret (%s or %s) must be set",
		ErrEnvironmentIncomplete, URLEnvVar, TokenEnvVar, SecretFileEnvVar, SecretEnvVar)
}

// CategoryOf classifies a cause — the error one of the engine's
// construction paths returned — into exactly one category. Every error
// has a category: a named cause classifies to its own, anything else to
// CategoryOther, and the absence of an error to
// CategoryNotConfigured (§forgejo/observability/why-the-engine-is-off).
func CategoryOf(cause error) Category {
	switch {
	case cause == nil, errors.Is(cause, ErrConfigNotFound):
		return CategoryNotConfigured
	case errors.Is(cause, ErrActionSetMismatch):
		return CategoryConfigBinaryMismatch
	case errors.Is(cause, ErrConfigUnreadable):
		return CategoryConfigUnreadable
	case errors.Is(cause, ErrEnvironmentIncomplete):
		return CategoryCredentialsMissing
	default:
		return CategoryOther
	}
}

// EngineStatus answers "why is the engine off?" from the config path
// and the environment alone: no store is opened, no client built, no
// contact made with the Forgejo instance, so a health probe may call it
// (§forgejo/observability/why-the-engine-is-off).
//
// The cause is non-nil exactly when the engine could not start; with it
// goes the category a surface prints. A nil cause means the engine is
// configured and would come up, and the category — CategoryNotConfigured,
// the zero value — carries no meaning. The classification is the same
// one startup makes: the same inputs, the same verdict.
func EngineStatus(configPath string, env Env) (cause error, category Category) {
	if _, err := LoadConfig(configPath); err != nil {
		return err, CategoryOf(err)
	}
	secret, err := loadSecret(env)
	if err != nil {
		// A secret file that exists but cannot be read, or that is
		// empty, is neither an absent config nor a missing credential.
		return err, CategoryOther
	}
	if err := CheckEnvironment(lookup(env, URLEnvVar), lookup(env, TokenEnvVar), secret); err != nil {
		return err, CategoryCredentialsMissing
	}
	return nil, CategoryNotConfigured
}
