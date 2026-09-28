package forgejo

import (
	"fmt"
	"os"
	"strings"
)

// SecretFileEnvVar is the environment variable naming the file that
// holds the webhook shared secret (§forgejo/webhook/configuration).
const SecretFileEnvVar = "MAITRED_FORGEJOENG_SECRET_FILE"

// SecretEnvVar is the transitional environment variable carrying the
// webhook shared secret directly (§forgejo/webhook/configuration). It
// is retained only so deployments can adopt the secret file
// incrementally, and is to be removed once every deployment reads the
// secret from a file.
const SecretEnvVar = "MAITRED_FORGEJOENG_SECRET"

// LoadSecret resolves the webhook shared secret
// (§forgejo/webhook/configuration): from the file named by
// SecretFileEnvVar — its contents, with a single trailing newline
// stripped — or, as a transitional measure, directly from
// SecretEnvVar. When both are set the file takes precedence. It
// returns ("", nil) when neither is set, so the caller can report the
// missing credentials alongside the rest; a secret file that is set
// but unreadable, or that yields an empty secret, is an error.
func LoadSecret() (string, error) {
	if path := os.Getenv(SecretFileEnvVar); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read shared secret file: %w", err)
		}
		secret := strings.TrimSuffix(string(data), "\n")
		if secret == "" {
			return "", fmt.Errorf("shared secret file %s is empty", path)
		}
		return secret, nil
	}
	return os.Getenv(SecretEnvVar), nil
}
