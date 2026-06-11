package sync

import (
	"fmt"
	"regexp"
	"strings"
)

// maskableRe matches values GitLab accepts for masked variables: at least
// 8 characters from the Base64-ish allowed alphabet (which excludes
// whitespace and newlines by construction).
var maskableRe = regexp.MustCompile(`^[a-zA-Z0-9+/=@:.~_-]{8,}$`)

// maskableViolation returns a human-readable reason why the value cannot
// be a masked GitLab variable, or "" when it is maskable. Pre-validating
// avoids opaque 400s from the API.
func maskableViolation(value string) string {
	switch {
	case len(value) < 8:
		return "masked value must be at least 8 characters"
	case strings.ContainsAny(value, "\n\r"):
		return "masked value must be a single line"
	case strings.Contains(value, " "):
		return "masked value must not contain spaces"
	case !maskableRe.MatchString(value):
		return fmt.Sprintf("masked value contains characters outside the allowed set %s", maskableRe)
	}
	return ""
}
