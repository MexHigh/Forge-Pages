package main

import "testing"

func TestNewURLPathHelper(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		expectedParts int
	}{
		{"empty path", "", 0},
		{"root path", "/", 0},
		{"single element", "user", 1},
		{"two elements", "user/repo", 2},
		{"with leading slash", "/user/repo", 2},
		{"with trailing slash", "user/repo/", 2},
		{"with both slashes", "/user/repo/", 2},
		{"multiple slashes", "//user//repo//", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := NewURLPathHelper(tt.url)
			if u.NumOfElements != tt.expectedParts {
				t.Errorf("NewURLPathHelper(%q).NumOfElements = %d, want %d",
					tt.url, u.NumOfElements, tt.expectedParts)
			}
		})
	}
}

func TestURLPathHelper_HasElement(t *testing.T) {
	u := NewURLPathHelper("user/repo/branch")

	if !u.HasElement(0) {
		t.Error("HasElement(0) = false, want true")
	}
	if !u.HasElement(2) {
		t.Error("HasElement(2) = false, want true")
	}
	if u.HasElement(3) {
		t.Error("HasElement(3) = true, want false")
	}
	if u.HasElement(10) {
		t.Error("HasElement(10) = true, want false")
	}
}

func TestURLPathHelper_GetElement(t *testing.T) {
	u := NewURLPathHelper("user/repo/branch/file.md")

	tests := []struct {
		index    int
		expected string
	}{
		{0, "user"},
		{1, "repo"},
		{2, "branch"},
		{3, "file.md"},
		{4, ""},  // out of bounds
		{10, ""}, // way out of bounds
	}

	for _, tt := range tests {
		if got := u.GetElement(tt.index); got != tt.expected {
			t.Errorf("GetElement(%d) = %q, want %q", tt.index, got, tt.expected)
		}
	}
}

func TestURLPathHelper_GetElementsStartingFromElement(t *testing.T) {
	u := NewURLPathHelper("user/repo/branch/src/main.go")

	tests := []struct {
		index    int
		expected string
	}{
		{0, "user/repo/branch/src/main.go"},
		{1, "repo/branch/src/main.go"},
		{2, "branch/src/main.go"},
		{3, "src/main.go"},
		{4, "main.go"},
		{5, ""}, // out of bounds returns empty
	}

	for _, tt := range tests {
		if got := u.GetElementsStartingFromElement(tt.index); got != tt.expected {
			t.Errorf("GetElementsStartingFromElement(%d) = %q, want %q",
				tt.index, got, tt.expected)
		}
	}
}

func TestURLPathHelper_EmptyPath(t *testing.T) {
	u := NewURLPathHelper("")

	if u.HasElement(0) {
		t.Error("HasElement(0) on empty path = true, want false")
	}
	if got := u.GetElement(0); got != "" {
		t.Errorf("GetElement(0) on empty path = %q, want empty", got)
	}
	if got := u.GetElementsStartingFromElement(0); got != "" {
		t.Errorf("GetElementsStartingFromElement(0) on empty path = %q, want empty", got)
	}
}