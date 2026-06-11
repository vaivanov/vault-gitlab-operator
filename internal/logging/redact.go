package logging

import "log/slog"

const redacted = "[REDACTED]"

// Secret holds a sensitive string value. Every accidental rendering path
// (slog, fmt %s/%v/%#v, JSON/text marshaling) yields "[REDACTED]"; the real
// value is only reachable through an explicit Reveal() call at the points
// where it is handed to the Vault or GitLab API.
type Secret string

func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

func (s Secret) String() string { return redacted }

func (s Secret) GoString() string { return redacted }

func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// Reveal returns the underlying sensitive value.
func (s Secret) Reveal() string { return string(s) }

// IsZero reports whether the secret is empty.
func (s Secret) IsZero() bool { return s == "" }
