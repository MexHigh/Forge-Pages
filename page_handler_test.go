package main

import (
	"net/http"
	"net/url"
	"testing"
)

func TestGetFullURL_BuildsFromConfig(t *testing.T) {
	config = &Config{
		PagesURL: "https://pages.example.com",
	}

	tests := []struct {
		name        string
		requestHost string
		requestPath string
		want        string
	}{
		{
			name:        "exact configured host",
			requestHost: "pages.example.com",
			requestPath: "/foo/bar",
			want:        "https://pages.example.com/foo/bar",
		},
		{
			name:        "subdomain route preserves owner prefix",
			requestHost: "leon.pages.example.com",
			requestPath: "/repo/asset.js",
			want:        "https://leon.pages.example.com/repo/asset.js",
		},
		{
			name:        "manipulated host is normalised to configured base",
			requestHost: "evil.com",
			requestPath: "/foo",
			want:        "https://pages.example.com/foo",
		},
		{
			name:        "scheme follows config even for plain HTTP request",
			requestHost: "pages.example.com",
			requestPath: "/",
			want:        "https://pages.example.com/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, _ := url.Parse(tt.requestPath)
			r := &http.Request{
				Host:   tt.requestHost,
				URL:    u,
				Header: make(http.Header),
			}
			got := getFullURL(r)
			if got != tt.want {
				t.Errorf("getFullURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetFullURL_CustomRoute(t *testing.T) {
	config = &Config{
		PagesURL: "https://pages.example.com",
		Routes: map[string]RouteConfig{
			"custom.example.com": {Owner: "leon", Repo: "site"},
		},
	}

	u, _ := url.Parse("/asset.css")
	r := &http.Request{
		Host:   "custom.example.com",
		URL:    u,
		Header: make(http.Header),
	}

	got := getFullURL(r)
	want := "https://custom.example.com/asset.css"
	if got != want {
		t.Errorf("getFullURL() = %q, want %q", got, want)
	}
}

func TestGetFullURL_HTTPConfig(t *testing.T) {
	config = &Config{
		PagesURL: "http://pages.example.com",
	}

	u, _ := url.Parse("/")
	r := &http.Request{
		Host:   "pages.example.com",
		URL:    u,
		Header: make(http.Header),
	}

	got := getFullURL(r)
	want := "http://pages.example.com/"
	if got != want {
		t.Errorf("getFullURL() = %q, want %q", got, want)
	}
}
