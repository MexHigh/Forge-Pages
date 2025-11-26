package main

import (
	"errors"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ForgeURL  string `yaml:"forge_url"`
	PagesURL  string `yaml:"pages_url"`
	ServePath string `yaml:"serve_path"`
	OIDC      struct {
		ID       string
		Secret   string
		AuthURL  string `yaml:"auth_url"`
		TokenURL string `yaml:"token_url"`
	} `yaml:"oidc"`
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
	if c.OIDC.ID == "" {
		return errors.New("oidc.id is required")
	}
	if c.OIDC.Secret == "" {
		return errors.New("oidc.secret is required")
	}
	if c.OIDC.AuthURL == "" {
		return errors.New("oidc.auth_url is required")
	}
	if c.OIDC.TokenURL == "" {
		return errors.New("oidc.token_url is required")
	}
	return nil
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if err := c.setDefaults(); err != nil {
		return nil, err
	}
	return &c, nil
}
