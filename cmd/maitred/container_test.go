package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// containerfilePath is the Containerfile relative to this package's
// directory (cmd/maitred).
func containerfilePath(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", "..", "Containerfile")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read Containerfile: %v", err)
	}
	return string(data)
}

// logicalLines joins backslash-continued lines so multi-line HEALTHCHECK
// instructions are seen as one, mirroring how a container runtime parses
// the file.
func logicalLines(content string) []string {
	var out []string
	var pending string
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if pending != "" {
			line = strings.TrimSpace(pending + " " + line)
		}
		if strings.HasSuffix(line, "\\") {
			pending = strings.TrimSuffix(line, "\\")
			continue
		}
		pending = ""
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

// healthcheckArgs returns the argv of the image HEALTHCHECK CMD (the
// shell command following the CMD keyword).
func healthcheckArgs(t *testing.T, content string) []string {
	t.Helper()
	for _, line := range logicalLines(content) {
		if !strings.HasPrefix(line, "HEALTHCHECK") {
			continue
		}
		idx := strings.Index(line, "CMD ")
		if idx < 0 {
			t.Fatalf("HEALTHCHECK has no CMD command: %q", line)
		}
		cmd := line[idx+4:]
		// Drop the shell error tail (`|| exit 1`) if present.
		if i := strings.Index(cmd, "||"); i >= 0 {
			cmd = cmd[:i]
		}
		return strings.Fields(cmd)
	}
	t.Fatal("Containerfile has no HEALTHCHECK instruction")
	return nil
}

// entrypointCMDArgs returns the argv array of the final exec-form CMD
// instruction — the arguments the running process actually receives
// (the ENTRYPOINT provides the binary).
func entrypointCMDArgs(t *testing.T, content string) []string {
	t.Helper()
	var argv []string
	for _, line := range logicalLines(content) {
		if !strings.HasPrefix(line, "CMD ") {
			continue
		}
		var arr []string
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "CMD "))), &arr); err != nil {
			t.Fatalf("CMD is not exec-form JSON: %q: %v", line, err)
		}
		argv = arr // keep the last CMD (the runtime stage's)
	}
	if argv == nil {
		t.Fatal("Containerfile has no CMD instruction")
	}
	return argv
}

// flagValue returns the value passed as a separate token after the given
// flag, or "" when the flag is absent.
func flagValue(argv []string, flag string) string {
	for i, tok := range argv {
		if tok == flag && i+1 < len(argv) {
			return argv[i+1]
		}
		if v, ok := strings.CutPrefix(tok, flag+"="); ok {
			return v
		}
	}
	return ""
}

// The HEALTHCHECK must probe the same directories the process runs on.
// The image's CMD passes them explicitly (config is mounted at
// /etc/maitred, state lives in /var/lib/maitred); `maitred --health`
// with no flags would instead fall back to defaults relative to
// WORKDIR, which the image does not ship — keeping the probe dark even
// when the process is healthy.
func TestContainerfileHealthcheckProbesRunningDirs(t *testing.T) {
	content := containerfilePath(t)

	hc := healthcheckArgs(t, content)
	if !containsToken(hc, "-health") && !containsToken(hc, "--health") {
		t.Fatalf("HEALTHCHECK does not invoke the health mode: %v", hc)
	}

	cmd := entrypointCMDArgs(t, content)
	for _, flag := range []string{"--trigger-dir", "--data-dir"} {
		want := flagValue(cmd, flag)
		if want == "" {
			t.Fatalf("CMD does not pass %s explicitly: %v", flag, cmd)
		}
		got := flagValue(hc, flag)
		if got != want {
			t.Errorf("HEALTHCHECK %s = %q, want %q (must match CMD)", flag, got, want)
		}
	}
}

func containsToken(argv []string, tok string) bool {
	for _, a := range argv {
		if a == tok {
			return true
		}
	}
	return false
}

// envValues returns the KEY=VALUE pairs declared by ENV instructions,
// keyed by name. Both the `ENV KEY=VALUE` form and the paired
// `ENV KEY VALUE` form are accepted.
func envValues(t *testing.T, content string) map[string]string {
	t.Helper()
	envs := make(map[string]string)
	for _, line := range logicalLines(content) {
		if !strings.HasPrefix(line, "ENV ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "ENV "))
		if key, val, ok := strings.Cut(rest, "="); ok {
			envs[strings.TrimSpace(key)] = strings.TrimSpace(val)
			continue
		}
		if fields := strings.Fields(rest); len(fields) == 2 {
			envs[fields[0]] = fields[1]
		}
	}
	return envs
}

// The image must also carry MAITRED_TRIGGER_DIR and MAITRED_DATA_DIR as
// ENVs naming the same directories the CMD passes, so flag-free
// invocations of the binary resolve the same configuration the process
// runs on (the issue's ENV alternative).
func TestContainerfileDeclaresDirEnvs(t *testing.T) {
	content := containerfilePath(t)

	envs := envValues(t, content)
	cmd := entrypointCMDArgs(t, content)
	for flag, key := range map[string]string{
		"--trigger-dir": "MAITRED_TRIGGER_DIR",
		"--data-dir":    "MAITRED_DATA_DIR",
	} {
		want := flagValue(cmd, flag)
		if want == "" {
			t.Fatalf("CMD does not pass %s explicitly: %v", flag, cmd)
		}
		got, ok := envs[key]
		if !ok {
			t.Errorf("Containerfile declares no ENV %s", key)
			continue
		}
		if got != want {
			t.Errorf("ENV %s = %q, want %q (must match CMD's %s)", key, got, want, flag)
		}
	}
}
