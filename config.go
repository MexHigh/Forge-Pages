package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

var config *Config

type RouteConfig struct {
	Owner string `yaml:"owner"`
	Repo  string `yaml:"repo"`
}

type Config struct {
	ForgeURL  string `yaml:"forge_url"`
	PagesURL  string `yaml:"pages_url"`
	ServePath string `yaml:"serve_path"`
	// MaxTotalExtractedBytes caps the cumulative uncompressed bytes written
	// by a single deploy's tar.gz extraction (see extractTarGz), on top of
	// the existing per-file and per-archive-file-count limits. Zero/unset
	// falls back to defaultMaxTotalExtractedBytes (256 MiB) -- see
	// maxTotalExtractedBytesLimit in deploy_ops.go, which applies that
	// fallback directly so the guard stays active even for a Config built
	// without calling setDefaults (as several tests do).
	MaxTotalExtractedBytes int64 `yaml:"max_total_extracted_bytes"`
	OAuth                  struct {
		ID       string
		Secret   string
		AuthURL  string `yaml:"auth_url"`
		TokenURL string `yaml:"token_url"`
	} `yaml:"oauth"`
	Routes map[string]RouteConfig `yaml:"routes"`
}

func (c *Config) setDefaults() error {
	if c.ForgeURL == "" {
		return errors.New("forge_url required")
	}
	if c.PagesURL == "" {
		return errors.New("pages_url required")
	}
	// Validate pages_url at load time so a malformed value fails fast at
	// startup rather than surfacing later on a request. The parsed scheme
	// itself is re-derived on demand by GetPagesURLScheme (not a hot path).
	if _, err := parsePagesURLScheme(c.PagesURL); err != nil {
		return fmt.Errorf("pages_url: %w", err)
	}
	if c.ServePath == "" {
		c.ServePath = "/srv"
	}
	if c.MaxTotalExtractedBytes <= 0 {
		c.MaxTotalExtractedBytes = defaultMaxTotalExtractedBytes
	}
	if c.OAuth.ID == "" {
		return errors.New("oidc.id is required")
	}
	if c.OAuth.Secret == "" {
		return errors.New("oidc.secret is required")
	}
	if c.OAuth.AuthURL == "" {
		return errors.New("oidc.auth_url is required")
	}
	if c.OAuth.TokenURL == "" {
		return errors.New("oidc.token_url is required")
	}
	return nil
}

func LoadConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return err
	}
	if err := c.setDefaults(); err != nil {
		return err
	}
	config = &c
	return nil
}

// parsePagesURLScheme parses rawURL and returns its scheme, requiring it to
// be "http" or "https" and requiring a non-empty host. This is the single
// place that decides whether a configured pages_url is well-formed enough
// to be trusted as the authoritative source of scheme+host for externally
// visible URLs (see GetPagesURLScheme and getFullURL in page_handler.go).
func parsePagesURLScheme(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid pages_url %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("pages_url %q must have an http or https scheme", rawURL)
	}
	if u.Host == "" {
		return "", fmt.Errorf("pages_url %q missing host", rawURL)
	}
	return u.Scheme, nil
}

// GetPagesURLScheme returns the scheme ("http" or "https") of the
// configured pages_url -- the authoritative source for the scheme used to
// build externally visible URLs (e.g. the OAuth2 post-login redirect
// target in getFullURL). Prefer this over inspecting request state such as
// r.TLS, which is nil for every request behind a TLS-terminating reverse
// proxy regardless of the actual public-facing scheme.
//
// It parses PagesURL on demand (only hit on protected-page requests, not a
// hot path), falling back to "http" if PagesURL is missing or malformed. A
// malformed pages_url has already been rejected at load time by setDefaults,
// so the fallback only applies to a Config built directly, bypassing
// setDefaults (as most tests do).
func (c *Config) GetPagesURLScheme() string {
	if scheme, err := parsePagesURLScheme(c.PagesURL); err == nil {
		return scheme
	}
	return "http"
}

func (c *Config) GetPagesURLHostOnly() string {
	return strings.ReplaceAll(strings.ReplaceAll(c.PagesURL, "https://", ""), "http://", "")
}

func (c *Config) GetPagesURLHostOnlyWithoutPort() string {
	host := c.GetPagesURLHostOnly()
	if strings.Contains(host, ":") {
		return strings.Split(host, ":")[0]
	} else {
		return host
	}
}

func (c *Config) GetPagesURLWithAdditionalSubdomain(subdomain string) string {
	return InsertStringAfterSubstring(c.PagesURL, `^(https?://)`, subdomain+".")
}
