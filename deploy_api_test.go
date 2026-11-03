package main

import (
	"net/http"
	"net/url"
	"testing"
)

func TestExtractAccessToken(t *testing.T) {
	tests := []struct {
		name        string
		headers     map[string]string
		queryValues url.Values
		want        string
	}{
		{
			name:        "token from Authorization header",
			headers:     map[string]string{"Authorization": "Bearer header-token"},
			queryValues: url.Values{"access_token": []string{"query-token"}},
			want:        "header-token",
		},
		{
			name:        "token from query parameter when no header",
			headers:     map[string]string{},
			queryValues: url.Values{"access_token": []string{"query-token"}},
			want:        "query-token",
		},
		{
			name:        "header wins over query parameter",
			headers:     map[string]string{"Authorization": "Bearer header-token"},
			queryValues: url.Values{"access_token": []string{"query-token"}},
			want:        "header-token",
		},
		{
			name:        "no token anywhere",
			headers:     map[string]string{},
			queryValues: url.Values{},
			want:        "",
		},
		{
			name:        "non-Bearer Authorization header ignored",
			headers:     map[string]string{"Authorization": "Basic dXNlcjpwYXNz"},
			queryValues: url.Values{"access_token": []string{"query-token"}},
			want:        "query-token",
		},
		{
			name:        "lowercase bearer prefix not matched",
			headers:     map[string]string{"Authorization": "bearer lowercase-token"},
			queryValues: url.Values{"access_token": []string{"query-token"}},
			want:        "query-token",
		},
		{
			name:        "trailing whitespace in header value is trimmed",
			headers:     map[string]string{"Authorization": "Bearer header-token   "},
			queryValues: url.Values{},
			want:        "header-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				URL:    &url.URL{RawQuery: tt.queryValues.Encode()},
				Header: make(http.Header),
			}
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			if got := extractAccessToken(req, tt.queryValues); got != tt.want {
				t.Errorf("extractAccessToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetParams_AccessTokenFromHeader(t *testing.T) {
	// Ensure deploy checks are enabled for this test.
	skip := false
	skipDeployChecks = &skip

	req := &http.Request{
		URL: &url.URL{
			RawQuery: url.Values{
				"repo":         []string{"owner/repo"},
				"access_token": []string{"query-token"},
			}.Encode(),
		},
		Header: http.Header{"Authorization": []string{"Bearer header-token"}},
	}

	repo, accessToken, addBasePath, protect, err := getParams(req)
	if err != nil {
		t.Fatalf("getParams() returned unexpected error: %v", err)
	}
	if repo != "owner/repo" {
		t.Errorf("repo = %q, want %q", repo, "owner/repo")
	}
	if accessToken != "header-token" {
		t.Errorf("accessToken = %q, want %q", accessToken, "header-token")
	}
	if addBasePath != "" {
		t.Errorf("addBasePath = %q, want empty", addBasePath)
	}
	if protect {
		t.Error("protect = true, want false")
	}
}

func TestGetParams_MissingAccessToken(t *testing.T) {
	skip := false
	skipDeployChecks = &skip

	req := &http.Request{
		URL: &url.URL{
			RawQuery: url.Values{"repo": []string{"owner/repo"}}.Encode(),
		},
		Header: make(http.Header),
	}

	_, _, _, _, err := getParams(req)
	if err == nil {
		t.Fatal("getParams() expected error for missing access token, got nil")
	}
	if err.Error() != "missing parameter: access_token" {
		t.Errorf("error = %q, want %q", err.Error(), "missing parameter: access_token")
	}
}

func TestGetParams_MissingRepo(t *testing.T) {
	req := &http.Request{
		URL:    &url.URL{RawQuery: url.Values{"access_token": []string{"token"}}.Encode()},
		Header: make(http.Header),
	}

	_, _, _, _, err := getParams(req)
	if err == nil {
		t.Fatal("getParams() expected error for missing repo, got nil")
	}
	if err.Error() != "missing parameter: repo" {
		t.Errorf("error = %q, want %q", err.Error(), "missing parameter: repo")
	}
}
