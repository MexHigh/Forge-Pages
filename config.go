package main

import (
	"errors"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

var config *Config

type Config struct {
	ForgeURL  string `yaml:"forge_url"`
	PagesURL  string `yaml:"pages_url"`
	ServePath string `yaml:"serve_path"`
	OAuth     struct {
		ID       string
		Secret   string
		AuthURL  string `yaml:"auth_url"`
		TokenURL string `yaml:"token_url"`
	} `yaml:"oauth"`
}

func (c *Config) setDefaults() error {
	if c.ForgeURL == "" {
		return errors.New("forge_url required")
	}
	if c.PagesURL == "" {
		return errors.New("pages_url required")
	}
	if c.ServePath == "" {
		c.ServePath = "/srv"
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
