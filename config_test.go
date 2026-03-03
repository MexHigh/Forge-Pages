package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfig_SetDefaults(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		expectError bool
		errorContains string
	}{
		{
			name:        "missing forge_url",
			config:      Config{},
			expectError: true,
			errorContains: "forge_url",
		},
		{
			name: "missing pages_url",
			config: Config{ForgeURL: "https://forge.example.com"},
			expectError: true,
			errorContains: "pages_url",
		},
		{
			name: "missing oauth id",
			config: Config{
				ForgeURL: "https://forge.example.com",
				PagesURL: "https://pages.example.com",
			},
			expectError: true,
			errorContains: "oidc.id",
		},
		{
			name: "missing oauth secret",
			config: Config{
				ForgeURL: "https://forge.example.com",
				PagesURL: "https://pages.example.com",
				OAuth: struct {
					ID       string
					Secret   string
					AuthURL  string `yaml:"auth_url"`
					TokenURL string `yaml:"token_url"`
				}{
					ID: "client-id",
				},
			},
			expectError: true,
			errorContains: "oidc.secret",
		},
		{
			name: "missing oauth auth_url",
			config: Config{
				ForgeURL: "https://forge.example.com",
				PagesURL: "https://pages.example.com",
				OAuth: struct {
					ID       string
					Secret   string
					AuthURL  string `yaml:"auth_url"`
					TokenURL string `yaml:"token_url"`
				}{
					ID:     "client-id",
					Secret: "client-secret",
				},
			},
			expectError: true,
			errorContains: "oidc.auth_url",
		},
		{
			name: "missing oauth token_url",
			config: Config{
				ForgeURL: "https://forge.example.com",
				PagesURL: "https://pages.example.com",
				OAuth: struct {
					ID       string
					Secret   string
					AuthURL  string `yaml:"auth_url"`
					TokenURL string `yaml:"token_url"`
				}{
					ID:      "client-id",
					Secret:  "client-secret",
					AuthURL: "https://auth.example.com",
				},
			},
			expectError: true,
			errorContains: "oidc.token_url",
		},
		{
			name: "valid config with all required fields",
			config: Config{
				ForgeURL:  "https://forge.example.com",
				PagesURL:  "https://pages.example.com",
				ServePath: "/srv/pages",
				OAuth: struct {
					ID       string
					Secret   string
					AuthURL  string `yaml:"auth_url"`
					TokenURL string `yaml:"token_url"`
				}{
					ID:       "client-id",
					Secret:   "client-secret",
					AuthURL:  "https://auth.example.com",
					TokenURL: "https://token.example.com",
				},
			},
			expectError: false,
		},
		{
			name: "default serve_path",
			config: Config{
				ForgeURL: "https://forge.example.com",
				PagesURL: "https://pages.example.com",
				OAuth: struct {
					ID       string
					Secret   string
					AuthURL  string `yaml:"auth_url"`
					TokenURL string `yaml:"token_url"`
				}{
					ID:       "client-id",
					Secret:   "client-secret",
					AuthURL:  "https://auth.example.com",
					TokenURL: "https://token.example.com",
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.setDefaults()
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errorContains)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestConfig_DefaultServePath(t *testing.T) {
	c := Config{
		ForgeURL: "https://forge.example.com",
		PagesURL: "https://pages.example.com",
		OAuth: struct {
			ID       string
			Secret   string
			AuthURL  string `yaml:"auth_url"`
			TokenURL string `yaml:"token_url"`
		}{
			ID:       "client-id",
			Secret:   "client-secret",
			AuthURL:  "https://auth.example.com",
			TokenURL: "https://token.example.com",
		},
	}

	if err := c.setDefaults(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c.ServePath != "/srv" {
		t.Errorf("ServePath = %q, want %q", c.ServePath, "/srv")
	}
}

func TestConfig_GetPagesURLHostOnly(t *testing.T) {
	tests := []struct {
		name     string
		pagesURL string
		expected string
	}{
		{"https without port", "https://pages.example.com", "pages.example.com"},
		{"http without port", "http://pages.example.com", "pages.example.com"},
		{"https with port", "https://pages.example.com:8443", "pages.example.com:8443"},
		{"http with port", "http://pages.example.com:8080", "pages.example.com:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{PagesURL: tt.pagesURL}
			if got := c.GetPagesURLHostOnly(); got != tt.expected {
				t.Errorf("GetPagesURLHostOnly() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestConfig_GetPagesURLHostOnlyWithoutPort(t *testing.T) {
	tests := []struct {
		name     string
		pagesURL string
		expected string
	}{
		{"https without port", "https://pages.example.com", "pages.example.com"},
		{"http without port", "http://pages.example.com", "pages.example.com"},
		{"https with port", "https://pages.example.com:8443", "pages.example.com"},
		{"http with port", "http://pages.example.com:8080", "pages.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{PagesURL: tt.pagesURL}
			if got := c.GetPagesURLHostOnlyWithoutPort(); got != tt.expected {
				t.Errorf("GetPagesURLHostOnlyWithoutPort() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestConfig_GetPagesURLWithAdditionalSubdomain(t *testing.T) {
	tests := []struct {
		name      string
		pagesURL  string
		subdomain string
		expected  string
	}{
		{
			name:      "add subdomain to https",
			pagesURL:  "https://pages.example.com",
			subdomain: "myrepo",
			expected:  "https://myrepo.pages.example.com",
		},
		{
			name:      "add subdomain to http",
			pagesURL:  "http://localhost:8080",
			subdomain: "test",
			expected:  "http://test.localhost:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{PagesURL: tt.pagesURL}
			if got := c.GetPagesURLWithAdditionalSubdomain(tt.subdomain); got != tt.expected {
				t.Errorf("GetPagesURLWithAdditionalSubdomain(%q) = %q, want %q",
					tt.subdomain, got, tt.expected)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yml")

	configContent := `
forge_url: https://forge.example.com
pages_url: https://pages.example.com
serve_path: /custom/path
oauth:
  id: test-client-id
  secret: test-client-secret
  auth_url: https://auth.example.com
  token_url: https://token.example.com
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	// Reset global config
	config = nil

	if err := LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if config == nil {
		t.Fatal("config is nil after LoadConfig")
	}

	if config.ForgeURL != "https://forge.example.com" {
		t.Errorf("ForgeURL = %q, want %q", config.ForgeURL, "https://forge.example.com")
	}
	if config.PagesURL != "https://pages.example.com" {
		t.Errorf("PagesURL = %q, want %q", config.PagesURL, "https://pages.example.com")
	}
	if config.ServePath != "/custom/path" {
		t.Errorf("ServePath = %q, want %q", config.ServePath, "/custom/path")
	}
	if config.OAuth.ID != "test-client-id" {
		t.Errorf("OAuth.ID = %q, want %q", config.OAuth.ID, "test-client-id")
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	config = nil
	err := LoadConfig("/nonexistent/config.yml")
	if err == nil {
		t.Error("LoadConfig should return error for nonexistent file")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.yml")

	if err := os.WriteFile(configPath, []byte("invalid: yaml: content: ["), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	config = nil
	err := LoadConfig(configPath)
	if err == nil {
		t.Error("LoadConfig should return error for invalid YAML")
	}
}