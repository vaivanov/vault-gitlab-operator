package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateCommand(t *testing.T) {
	fixtures := filepath.Join("..", "..", "internal", "config", "testdata")

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"valid config", []string{"validate", "-c", filepath.Join(fixtures, "valid_full.yaml")}, exitOK},
		{"valid minimal", []string{"validate", "-c", filepath.Join(fixtures, "valid_minimal.yaml")}, exitOK},
		{"missing file", []string{"validate", "-c", filepath.Join(fixtures, "nope.yaml")}, exitConfigError},
		{"bad log level", []string{"validate", "--log-level", "loud", "-c", filepath.Join(fixtures, "valid_minimal.yaml")}, exitConfigError},
		{"unknown flag", []string{"validate", "--bogus"}, exitConfigError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(tt.args); got != tt.want {
				t.Errorf("run(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}

func TestValidateInvalidConfigExitCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := writeFile(path, "gitlab: {url: https://g}\ntargets: {}\n"); err != nil {
		t.Fatal(err)
	}
	if got := run([]string{"validate", "-c", path}); got != exitConfigError {
		t.Errorf("exit = %d, want %d", got, exitConfigError)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
