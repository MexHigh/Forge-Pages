package main

import "testing"

func TestInsertStringAfterSubstring(t *testing.T) {
	tests := []struct {
		name      string
		str       string
		substring string
		insert    string
		expected  string
	}{
		{
			name:      "insert subdomain after https://",
			str:       "https://example.com",
			substring: `^(https?://)`,
			insert:    "sub.",
			expected:  "https://sub.example.com",
		},
		{
			name:      "insert subdomain after http://",
			str:       "http://example.com",
			substring: `^(https?://)`,
			insert:    "api.",
			expected:  "http://api.example.com",
		},
		{
			name:      "no match returns original",
			str:       "ftp://example.com",
			substring: `^(https?://)`,
			insert:    "sub.",
			expected:  "ftp://example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InsertStringAfterSubstring(tt.str, tt.substring, tt.insert)
			if got != tt.expected {
				t.Errorf("InsertStringAfterSubstring(%q, %q, %q) = %q, want %q",
					tt.str, tt.substring, tt.insert, got, tt.expected)
			}
		})
	}
}
