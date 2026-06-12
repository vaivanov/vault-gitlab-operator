package logging

import "log/slog"

const redacted = "[REDACTED]"

// Secret holds a sensitive string value. Every accidental rendering path
// (slog, fmt %s/%v/%#v, JSON/text marshaling) yields "[REDACTED]"; the real
// value is only reachable through an explicit Reveal() call at the points
// where it is handed to the Vault or GitLab API.
type Secret string

// LogValue redacts the secret in slog output.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// String redacts the secret in fmt %s/%v output.
func (s Secret) String() string { return redacted }

// GoString redacts the secret in fmt %#v output.
func (s Secret) GoString() string { return redacted }

// MarshalText redacts the secret in text-based encoders.
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// MarshalJSON redacts the secret in JSON encoding.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// Reveal returns the underlying sensitive value.
func (s Secret) Reveal() string { return string(s) }

// IsZero reports whether the secret is empty.
func (s Secret) IsZero() bool { return s == "" }
