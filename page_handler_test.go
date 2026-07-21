package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHandlePage_DoesNotLeakQueryTokenInLogs proves that a page request whose
// URL carries a sensitive "?access_token=SECRET" query parameter never leaks
// that value into the page handler's log output: every r.URL log site is
// routed through safeURLForLogging, which redacts the token.
func TestHandlePage_DoesNotLeakQueryTokenInLogs(t *testing.T) {
	setupPageHandlerTestConfig(t)

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOutput)
		log.SetFlags(prevFlags)
	})

	// request a page that does not exist, so the handler logs the (redacted)
	// request URL on the "does not exist" path
	w := httptest.NewRecorder()
	handlePage(w, pageRequest("owner1.pages.example.com", "/repo1/index.html?access_token=SECRETVALUE"))

	logs := logBuf.String()
	if strings.Contains(logs, "SECRETVALUE") {
		t.Fatalf("page handler log leaked the access_token value: %q", logs)
	}
	if !strings.Contains(logs, "access_token=REDACTED") {
		t.Fatalf("expected redacted access_token marker in page handler log, got: %q", logs)
	}
}

// setupPageHandlerTestConfig points the global config at a fresh temp
// ServePath and PagesURL, and restores the previous global config on test
// cleanup, mirroring setupDeployTestConfig in deploy_api_test.go.
func setupPageHandlerTestConfig(t *testing.T) (servePath string) {
	t.Helper()
	servePath = t.TempDir()
	prevConfig := config
	config = &Config{
		PagesURL:  "https://pages.example.com",
		ServePath: servePath,
	}
	t.Cleanup(func() { config = prevConfig })
	return servePath
}

// pageRequest builds a request the way net/http's server actually delivers
// one: r.URL holds only path+query (origin-form request line), with no
// scheme or host -- those come from r.Host (and, for a real TLS-terminated
// request behind a reverse proxy, are never reflected in r.TLS). Building
// the target as an absolute "http://host/path" URL instead would leak a
// scheme+host into r.URL that a real server request never has, masking
// bugs in code (like getFullURL) that must derive scheme/host from
// elsewhere.
func pageRequest(host, path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	return req
}

// TestHandlePage_NestedAssetServed proves a normal nested asset (e.g.
// css/site.css) inside a deployed page still serves correctly now that
// asset path resolution is routed through pathsafe.SecureJoin instead of a
// bare filepath.Join+Clean.
func TestHandlePage_NestedAssetServed(t *testing.T) {
	servePath := setupPageHandlerTestConfig(t)

	assetDir := filepath.Join(servePath, "owner1", "repo1", "__ROOT__", "css")
	if err := os.MkdirAll(assetDir, 0750); err != nil {
		t.Fatalf("failed to seed asset dir: %v", err)
	}
	const cssContent = "body { color: red; }"
	if err := os.WriteFile(filepath.Join(assetDir, "site.css"), []byte(cssContent), 0640); err != nil {
		t.Fatalf("failed to seed asset file: %v", err)
	}

	w := httptest.NewRecorder()
	handlePage(w, pageRequest("owner1.pages.example.com", "/repo1/css/site.css"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for legitimate nested asset, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != cssContent {
		t.Fatalf("unexpected asset body: got %q, want %q", w.Body.String(), cssContent)
	}
}

// TestHandlePage_AssetPathEscapeRejected proves a crafted, attacker-supplied
// asset path (nested ".." segments deep enough to escape the page's
// StoragePath) is rejected via pathsafe.SecureJoin, with a 404 -- not the
// generic 400 that net/http's ServeFile would otherwise return for a
// literal ".." in the URL path. Getting 404 here (rather than 400)
// confirms the SecureJoin containment check fires before ServeFile is ever
// called, i.e. containment no longer depends on the implicit net/http
// dotdot guard inside ServeFile.
func TestHandlePage_AssetPathEscapeRejected(t *testing.T) {
	servePath := setupPageHandlerTestConfig(t)

	// seed the legitimate page so page.Exists() succeeds and we reach the
	// asset-serving code path
	pageDir := filepath.Join(servePath, "owner2", "repo2", "__ROOT__")
	if err := os.MkdirAll(pageDir, 0750); err != nil {
		t.Fatalf("failed to seed page dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pageDir, "index.html"), []byte("hi"), 0640); err != nil {
		t.Fatalf("failed to seed index.html: %v", err)
	}

	// seed a secret file outside the page's StoragePath that a successful
	// escape would read: 3 levels of ".." from
	// <servePath>/owner2/repo2/__ROOT__ lands exactly at <servePath>/secret.txt
	secretPath := filepath.Join(servePath, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("top secret"), 0640); err != nil {
		t.Fatalf("failed to seed secret file: %v", err)
	}

	w := httptest.NewRecorder()
	handlePage(w, pageRequest("owner2.pages.example.com", "/repo2/../../../secret.txt"))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for asset path escape attempt, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() == "top secret" {
		t.Fatalf("secret file contents were leaked in response body")
	}
}

// TestGetFullURL_UsesConfiguredSchemeNotTLS proves getFullURL derives the
// scheme from the configured pages_url, not from r.TLS. Behind a
// TLS-terminating reverse proxy r.TLS is always nil on the
// request this service actually receives, even though the public-facing
// connection is HTTPS -- deriving scheme from r.TLS would silently build an
// "http://" OAuth return URL that mismatches the "https://" redirect_uri
// registered with the provider, breaking the protected-page login flow.
func TestGetFullURL_UsesConfiguredSchemeNotTLS(t *testing.T) {
	setupPageHandlerTestConfig(t) // pages_url: https://pages.example.com

	req := pageRequest("owner.pages.example.com", "/repo1/index.html?foo=bar")
	if req.TLS != nil {
		t.Fatalf("test precondition violated: r.TLS must be nil")
	}

	got, ok := getFullURL(req)
	if !ok {
		t.Fatalf("getFullURL rejected a valid owner-subdomain host")
	}
	const want = "https://owner.pages.example.com/repo1/index.html?foo=bar"
	if got != want {
		t.Fatalf("getFullURL() = %q, want %q", got, want)
	}
}

// TestGetFullURL_BaseHostAccepted proves the base pages_url host itself
// (no owner subdomain) is accepted and still gets the configured scheme.
func TestGetFullURL_BaseHostAccepted(t *testing.T) {
	setupPageHandlerTestConfig(t) // pages_url: https://pages.example.com

	req := pageRequest("pages.example.com", "/")
	got, ok := getFullURL(req)
	if !ok {
		t.Fatalf("getFullURL rejected the base pages_url host")
	}
	const want = "https://pages.example.com/"
	if got != want {
		t.Fatalf("getFullURL() = %q, want %q", got, want)
	}
}

// TestGetFullURL_RejectsUnrelatedHost proves a Host header that is neither
// the configured base pages_url host nor a single-label subdomain of it is
// rejected outright -- getFullURL must never build a redirect target from
// an attacker-supplied Host, which would otherwise let a crafted request
// redirect the OAuth flow to an arbitrary domain (host-header injection /
// open redirect).
func TestGetFullURL_RejectsUnrelatedHost(t *testing.T) {
	setupPageHandlerTestConfig(t) // pages_url: https://pages.example.com

	req := pageRequest("evil.example.net", "/repo1/index.html")
	got, ok := getFullURL(req)
	if ok {
		t.Fatalf("getFullURL accepted unrelated host, built %q", got)
	}
	if got != "" {
		t.Fatalf("getFullURL returned non-empty URL %q on rejection", got)
	}
}

// TestGetFullURL_RejectsMultiLabelSubdomain proves a multi-level subdomain
// (more than one DNS label in front of the base host) is rejected, not
// silently accepted as if the extra dots were part of a single owner
// label. Without this, "a.b.pages.example.com" would strip to owner label
// "a.b" and validate incorrectly.
func TestGetFullURL_RejectsMultiLabelSubdomain(t *testing.T) {
	setupPageHandlerTestConfig(t) // pages_url: https://pages.example.com

	req := pageRequest("a.b.pages.example.com", "/repo1/index.html")
	if _, ok := getFullURL(req); ok {
		t.Fatalf("getFullURL accepted a multi-label subdomain host")
	}
}

// TestHandlePage_InvalidHostRejected is the handler-level counterpart of
// TestGetFullURL_RejectsUnrelatedHost: hitting handlePage directly with a
// Host unrelated to the configured pages_url must be rejected with 400
// before any redirect (OAuth or otherwise) is ever constructed.
func TestHandlePage_InvalidHostRejected(t *testing.T) {
	setupPageHandlerTestConfig(t)

	w := httptest.NewRecorder()
	handlePage(w, pageRequest("evil.example.net", "/repo1/index.html"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unrelated host, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Header().Get("Location"), "evil.example.net") {
		t.Fatalf("response must not redirect toward the untrusted host")
	}
}
