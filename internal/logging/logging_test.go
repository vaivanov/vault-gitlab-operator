package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSetupLevels(t *testing.T) {
	tests := []struct {
		level, format string
		wantErr       bool
	}{
		{"debug", "text", false},
		{"info", "json", false},
		{"", "", false},
		{"warn", "text", false},
		{"warning", "text", false},
		{"error", "json", false},
		{"DEBUG", "JSON", false},
		{"verbose", "text", true},
		{"info", "xml", true},
	}
	for _, tt := range tests {
		_, err := Setup(&bytes.Buffer{}, tt.level, tt.format)
		if gotErr := err != nil; gotErr != tt.wantErr {
			t.Errorf("Setup(level=%q, format=%q) error = %v, wantErr %v", tt.level, tt.format, err, tt.wantErr)
		}
	}
}

func TestSetupLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log, err := Setup(&buf, "warn", "text")
	if err != nil {
		t.Fatal(err)
	}
	log.Info("should be dropped")
	log.Warn("should appear")
	out := buf.String()
	if strings.Contains(out, "should be dropped") {
		t.Errorf("info message leaked through warn level: %q", out)
	}
	if !strings.Contains(out, "should appear") {
		t.Errorf("warn message missing: %q", out)
	}
}

func TestSecretNeverRenders(t *testing.T) {
	const sensitive = "s3cr3t-value-XYZ"
	s := Secret(sensitive)

	renderings := map[string]string{
		"String()":   s.String(),
		"fmt %s":     fmt.Sprintf("%s", s),
		"fmt %v":     fmt.Sprintf("%v", s),
		"fmt %#v":    fmt.Sprintf("%#v", s),
		"fmt %q":     fmt.Sprintf("%q", s),
	}
	for name, got := range renderings {
		if strings.Contains(got, sensitive) {
			t.Errorf("%s leaked secret: %q", name, got)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("%s did not redact: %q", name, got)
		}
	}

	if b, _ := json.Marshal(s); strings.Contains(string(b), sensitive) {
		t.Errorf("json.Marshal leaked secret: %s", b)
	}
	if b, _ := s.MarshalText(); strings.Contains(string(b), sensitive) {
		t.Errorf("MarshalText leaked secret: %s", b)
	}

	if s.Reveal() != sensitive {
		t.Errorf("Reveal() = %q, want %q", s.Reveal(), sensitive)
	}
	if s.IsZero() {
		t.Error("IsZero() = true for non-empty secret")
	}
	if !Secret("").IsZero() {
		t.Error("IsZero() = false for empty secret")
	}
}

func TestSecretRedactedInSlog(t *testing.T) {
	const sensitive = "super-secret-token-123"
	for _, format := range []string{"text", "json"} {
		var buf bytes.Buffer
		log, err := Setup(&buf, "debug", format)
		if err != nil {
			t.Fatal(err)
		}
		log.Info("logging in", "token", Secret(sensitive), "nested", slog_group(sensitive))
		if strings.Contains(buf.String(), sensitive) {
			t.Errorf("format=%s: slog output leaked secret: %q", format, buf.String())
		}
		if !strings.Contains(buf.String(), "[REDACTED]") {
			t.Errorf("format=%s: slog output missing redaction marker: %q", format, buf.String())
		}
	}
}

// slog_group returns an any wrapping a Secret to ensure redaction holds
// through interface boxing too.
func slog_group(v string) any { return Secret(v) }
