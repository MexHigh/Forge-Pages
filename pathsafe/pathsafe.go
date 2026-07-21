// Package pathsafe provides small, dependency-free helpers for validating
// user-supplied path components (repo/owner labels, tar entry segments)
// before they are used to build filesystem paths, preventing directory
// traversal and cross-tenant path collisions.
package pathsafe

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var labelRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9._-]{0,98}[a-zA-Z0-9])?$`)

// SafeLabel validates that s is a single, non-empty, DNS-label-ish path
// component: it must match labelRe. It rejects path separators, empty
// strings, and traversal sequences. labelRe requires a leading alphanumeric
// character, so "", ".", and ".." are all rejected by the pattern itself --
// no separate equality check for "."/".." is needed here (unlike SecureJoin,
// where the parts never pass through labelRe and those checks are required).
func SafeLabel(s string) error {
	if !labelRe.MatchString(s) {
		return fmt.Errorf("invalid path label %q", s)
	}
	return nil
}

// SecureJoin joins root with parts, rejecting any part that is empty,
// absolute, a "." or ".." traversal segment, or contains a path separator,
// and verifying that the resulting path stays within root (i.e.
// filepath.Rel(root, joined) does not escape via "..").
func SecureJoin(root string, parts ...string) (string, error) {
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || filepath.IsAbs(p) || strings.ContainsAny(p, `/\`) {
			return "", fmt.Errorf("invalid path element %q", p)
		}
	}
	joined := filepath.Join(append([]string{root}, parts...)...)
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}
	return joined, nil
}
