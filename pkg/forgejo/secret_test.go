package forgejo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSecretFile writes content to a fresh file in a temp directory and
// returns its path.
func writeSecretFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	return path
}

func TestLoadSecret_FileTakesPrecedence(t *testing.T) {
	t.Setenv(SecretFileEnvVar, writeSecretFile(t, "file-secret\n"))
	t.Setenv(SecretEnvVar, "env-secret")

	secret, err := LoadSecret()
	if err != nil {
		t.Fatalf("LoadSecret() error = %v", err)
	}
	if secret != "file-secret" {
		t.Fatalf("LoadSecret() = %q, want %q (file takes precedence)", secret, "file-secret")
	}
}

func TestLoadSecret_FileOnly(t *testing.T) {
	t.Setenv(SecretFileEnvVar, writeSecretFile(t, "file-secret\n"))
	t.Setenv(SecretEnvVar, "")

	secret, err := LoadSecret()
	if err != nil {
		t.Fatalf("LoadSecret() error = %v", err)
	}
	if secret != "file-secret" {
		t.Fatalf("LoadSecret() = %q, want %q (single trailing newline stripped)", secret, "file-secret")
	}
}

func TestLoadSecret_FileWithoutTrailingNewline(t *testing.T) {
	t.Setenv(SecretFileEnvVar, writeSecretFile(t, "no-newline"))
	t.Setenv(SecretEnvVar, "")

	secret, err := LoadSecret()
	if err != nil {
		t.Fatalf("LoadSecret() error = %v", err)
	}
	if secret != "no-newline" {
		t.Fatalf("LoadSecret() = %q, want %q (unchanged)", secret, "no-newline")
	}
}

func TestLoadSecret_FileStripsOnlyOneTrailingNewline(t *testing.T) {
	t.Setenv(SecretFileEnvVar, writeSecretFile(t, "secret\n\n"))
	t.Setenv(SecretEnvVar, "")

	secret, err := LoadSecret()
	if err != nil {
		t.Fatalf("LoadSecret() error = %v", err)
	}
	if secret != "secret\n" {
		t.Fatalf("LoadSecret() = %q, want %q (only one trailing newline stripped)", secret, "secret\n")
	}
}

func TestLoadSecret_EnvOnly(t *testing.T) {
	t.Setenv(SecretFileEnvVar, "")
	t.Setenv(SecretEnvVar, "env-secret")

	secret, err := LoadSecret()
	if err != nil {
		t.Fatalf("LoadSecret() error = %v", err)
	}
	if secret != "env-secret" {
		t.Fatalf("LoadSecret() = %q, want %q", secret, "env-secret")
	}
}

func TestLoadSecret_EmptyFileVarFallsBackToEnv(t *testing.T) {
	t.Setenv(SecretFileEnvVar, "")
	t.Setenv(SecretEnvVar, "env-secret")

	secret, err := LoadSecret()
	if err != nil {
		t.Fatalf("LoadSecret() error = %v", err)
	}
	if secret != "env-secret" {
		t.Fatalf("LoadSecret() = %q, want %q (empty file var is unset)", secret, "env-secret")
	}
}

func TestLoadSecret_FileMissing(t *testing.T) {
	t.Setenv(SecretFileEnvVar, filepath.Join(t.TempDir(), "does-not-exist"))
	t.Setenv(SecretEnvVar, "")

	secret, err := LoadSecret()
	if err == nil {
		t.Fatalf("LoadSecret() = %q, want error for missing file", secret)
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error %q does not name the missing file", err)
	}
}

func TestLoadSecret_FileEmpty(t *testing.T) {
	t.Setenv(SecretFileEnvVar, writeSecretFile(t, ""))
	t.Setenv(SecretEnvVar, "")

	if secret, err := LoadSecret(); err == nil {
		t.Fatalf("LoadSecret() = %q, want error for empty file", secret)
	}
}

func TestLoadSecret_FileOnlyNewline(t *testing.T) {
	t.Setenv(SecretFileEnvVar, writeSecretFile(t, "\n"))
	t.Setenv(SecretEnvVar, "")

	if secret, err := LoadSecret(); err == nil {
		t.Fatalf("LoadSecret() = %q, want error for file holding only a newline", secret)
	}
}

func TestLoadSecret_NeitherSet(t *testing.T) {
	t.Setenv(SecretFileEnvVar, "")
	t.Setenv(SecretEnvVar, "")

	secret, err := LoadSecret()
	if err != nil {
		t.Fatalf("LoadSecret() error = %v, want nil when neither is set", err)
	}
	if secret != "" {
		t.Fatalf("LoadSecret() = %q, want empty when neither is set", secret)
	}
}
