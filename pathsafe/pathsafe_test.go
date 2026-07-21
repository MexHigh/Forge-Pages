package pathsafe

import "testing"

func TestSafeLabelRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"..", ".", "a/b", `a\b`, "../x", "", "a..b/../c", "/abs"} {
		if err := SafeLabel(bad); err == nil {
			t.Errorf("SafeLabel(%q) = nil, want error", bad)
		}
	}
	for _, good := range []string{"repo", "my-repo", "v1.2.3", "pr-42"} {
		if err := SafeLabel(good); err != nil {
			t.Errorf("SafeLabel(%q) = %v, want nil", good, err)
		}
	}
}

func TestSecureJoinContainment(t *testing.T) {
	root := t.TempDir()
	if _, err := SecureJoin(root, "owner", "repo", "__ROOT__"); err != nil {
		t.Fatalf("legit join failed: %v", err)
	}
	for _, parts := range [][]string{
		{"..", "..", "ownerB", "repoB"},
		{"owner", "../../etc"},
		{"owner", "repo/../../.."},
		{"/abs", "x"},
		// A standalone "." or ".." component must be rejected even when the
		// fully-joined path happens to stay within root: the join must never
		// be built from traversal segments in the first place.
		{".."},
		{"."},
		{"a", "..", "b"},
		{"a", ".", "b"},
		{"a", "b", ".."},
	} {
		if _, err := SecureJoin(root, parts...); err == nil {
			t.Errorf("SecureJoin(%v) = nil error, want containment error", parts)
		}
	}
}
