package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEnv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// unset clears key for the test and restores its original value afterwards.
func unset(t *testing.T, key string) {
	t.Setenv(key, "")
	os.Unsetenv(key)
}

func TestLoadDotEnv(t *testing.T) {
	for _, k := range []string{"LLMPROXY_T_PLAIN", "LLMPROXY_T_EXPORT", "LLMPROXY_T_DOUBLE", "LLMPROXY_T_SINGLE", "LLMPROXY_T_COMMENT"} {
		unset(t, k)
	}
	t.Setenv("LLMPROXY_T_SHELL", "from-shell")

	path := writeEnv(t, `
# a comment
LLMPROXY_T_PLAIN=9000
export LLMPROXY_T_EXPORT=yes
LLMPROXY_T_DOUBLE="hello # not a comment"
LLMPROXY_T_SINGLE='single'
LLMPROXY_T_COMMENT=value # trailing comment
LLMPROXY_T_SHELL=from-file
`)
	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"LLMPROXY_T_PLAIN":   "9000",
		"LLMPROXY_T_EXPORT":  "yes",
		"LLMPROXY_T_DOUBLE":  "hello # not a comment",
		"LLMPROXY_T_SINGLE":  "single",
		"LLMPROXY_T_COMMENT": "value",
		"LLMPROXY_T_SHELL":   "from-shell", // the environment wins over the file
	}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestLoadDotEnvMissingFile(t *testing.T) {
	if err := loadDotEnv(filepath.Join(t.TempDir(), ".env")); err != nil {
		t.Errorf("missing file: %v", err)
	}
}

func TestLoadDotEnvBadLine(t *testing.T) {
	if err := loadDotEnv(writeEnv(t, "NOT A VALID LINE\n")); err == nil {
		t.Error("expected an error for a line without =")
	}
}
