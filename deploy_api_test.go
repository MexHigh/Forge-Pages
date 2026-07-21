package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// stubForgejoWritable starts a local httptest server that stands in for the
// Forgejo instance and unconditionally grants push (write) access to any
// repo it's asked about. This lets handleNewDeployment/handleDeleteDeployment
// run through their real ForgeCheckRepoWritableWithAccessToken code path
// without needing *skipDeployChecks (which would bypass permission checking
// entirely and defeat the purpose of an API-level regression test).
func stubForgejoWritable(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var resp GetRepoEndpointResponse
		resp.ID = 1
		resp.Name = "repo"
		resp.Permissions.Push = true
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("failed to encode stub forgejo response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setupDeployTestConfig points the global config at a fresh temp ServePath
// and the given (stub) Forgejo URL, and restores the previous global config
// on test cleanup so tests don't leak state into each other.
func setupDeployTestConfig(t *testing.T, forgeURL string) (servePath string) {
	t.Helper()
	servePath = t.TempDir()
	prevConfig := config
	config = &Config{
		ForgeURL:  forgeURL,
		PagesURL:  "https://pages.example.com",
		ServePath: servePath,
	}
	t.Cleanup(func() { config = prevConfig })
	return servePath
}

// makeTarGz builds an in-memory .tar.gz archive from a name->content map, in
// the iteration order given by names, so callers can control entry ordering.
func makeTarGz(t *testing.T, names []string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		content := files[name]
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar WriteHeader(%q): %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar Write(%q): %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

// deployRequest builds a POST /deploy request carrying the deploy token via
// the "Authorization: Bearer" header, matching the contract that the token
// must never be present in the query string, only the header. query must not
// itself set "access_token"/"token"; tests that need to exercise the legacy
// query-token path build their request by hand (see
// TestHandleDeploy_QueryTokenOnly_Rejected).
func deployRequest(query url.Values, body []byte) *http.Request {
	target := "http://pages.example.com/deploy?" + query.Encode()
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	req.Host = "pages.example.com"
	req.Header.Set("Authorization", "Bearer faketoken")
	return req
}

// deleteRequest builds a DELETE /deploy request; see deployRequest's doc
// comment for the Authorization-header-only token contract this follows.
func deleteRequest(query url.Values) *http.Request {
	target := "http://pages.example.com/deploy?" + query.Encode()
	req := httptest.NewRequest(http.MethodDelete, target, nil)
	req.Host = "pages.example.com"
	req.Header.Set("Authorization", "Bearer faketoken")
	return req
}

// assertNoStagingOrOldDirsRemain fails the test if any leftover
// ".staging-*" or ".old-*" sibling directories (the staged-deploy
// intermediates from createStagingDir/promoteStaging) are found directly
// under ownerRepoDir (i.e. next to the "__ROOT__" or additional-base-path
// directory). Both success and failure deploy paths must clean these up --
// success by consuming staging via rename and removing the old backup,
// failure by removing staging and never having touched the live target.
func assertNoStagingOrOldDirsRemain(t *testing.T, ownerRepoDir string) {
	t.Helper()
	staging, err := filepath.Glob(filepath.Join(ownerRepoDir, "*.staging-*"))
	if err != nil {
		t.Fatalf("glob staging dirs under %s: %v", ownerRepoDir, err)
	}
	if len(staging) != 0 {
		t.Fatalf("staging directories left behind under %s: %v", ownerRepoDir, staging)
	}
	old, err := filepath.Glob(filepath.Join(ownerRepoDir, "*.old-*"))
	if err != nil {
		t.Fatalf("glob old-backup dirs under %s: %v", ownerRepoDir, err)
	}
	if len(old) != 0 {
		t.Fatalf("old-backup directories left behind under %s: %v", ownerRepoDir, old)
	}
}

// TestHandleDeploy_LegitDeployment is a positive control proving the stub
// Forgejo + full handleDeploy code path works, so the rejection tests below
// are known to be exercising real containment logic and not just failing
// for an unrelated reason (e.g. a broken permission check).
func TestHandleDeploy_LegitDeployment(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)

	body := makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": "<h1>hi</h1>"})
	q := url.Values{}
	q.Set("repo", "owner1/repo1")

	w := httptest.NewRecorder()
	handleDeploy(w, deployRequest(q, body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	deployedFile := filepath.Join(servePath, "owner1", "repo1", "__ROOT__", "index.html")
	if _, err := os.Stat(deployedFile); err != nil {
		t.Fatalf("expected deployed file at %s: %v", deployedFile, err)
	}

	assertNoStagingOrOldDirsRemain(t, filepath.Join(servePath, "owner1", "repo1"))
}

// TestHandleDeploy_CrossRepoOverwriteViaAdditionalBasePathRejected proves
// that a malicious additional_base_path can no longer be used to escape the
// (owner, repo) sandbox and overwrite a *different* repo's deployed page --
// the vulnerability this task fixes. Before the fix, additional_base_path
// was joined into the storage path completely unvalidated, so
// "../../victimowner/victimrepo" would resolve to
// <ServePath>/victimowner/victimrepo, i.e. someone else's page directory.
func TestHandleDeploy_CrossRepoOverwriteViaAdditionalBasePathRejected(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)

	// pre-seed a legitimate "victim" deployment
	victimDir := filepath.Join(servePath, "victimowner", "victimrepo", "__ROOT__")
	if err := os.MkdirAll(victimDir, 0750); err != nil {
		t.Fatalf("failed to seed victim dir: %v", err)
	}
	victimFile := filepath.Join(victimDir, "index.html")
	const victimContent = "victim content, do not overwrite"
	if err := os.WriteFile(victimFile, []byte(victimContent), 0640); err != nil {
		t.Fatalf("failed to seed victim file: %v", err)
	}

	body := makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": "pwned"})
	q := url.Values{}
	q.Set("repo", "attackerowner/attackerrepo")
	q.Set("additional_base_path", "../../victimowner/victimrepo")

	w := httptest.NewRecorder()
	handleDeploy(w, deployRequest(q, body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for cross-repo overwrite attempt, got %d: %s", w.Code, w.Body.String())
	}

	got, err := os.ReadFile(victimFile)
	if err != nil {
		t.Fatalf("victim file was removed/inaccessible after rejected attack: %v", err)
	}
	if string(got) != victimContent {
		t.Fatalf("victim file was overwritten: got %q, want %q", got, victimContent)
	}
}

// TestHandleDelete_CrossRepoDeleteViaAdditionalBasePathRejected is the
// delete-side counterpart: additional_base_path must not be usable to
// os.RemoveAll a different repo's directory.
func TestHandleDelete_CrossRepoDeleteViaAdditionalBasePathRejected(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)

	victimDir := filepath.Join(servePath, "victimowner", "victimrepo", "__ROOT__")
	if err := os.MkdirAll(victimDir, 0750); err != nil {
		t.Fatalf("failed to seed victim dir: %v", err)
	}
	victimFile := filepath.Join(victimDir, "index.html")
	if err := os.WriteFile(victimFile, []byte("victim content"), 0640); err != nil {
		t.Fatalf("failed to seed victim file: %v", err)
	}

	q := url.Values{}
	q.Set("repo", "attackerowner/attackerrepo")
	q.Set("additional_base_path", "../../victimowner/victimrepo")

	w := httptest.NewRecorder()
	handleDeploy(w, deleteRequest(q))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for cross-repo delete attempt, got %d: %s", w.Code, w.Body.String())
	}

	if _, err := os.Stat(victimFile); err != nil {
		t.Fatalf("victim file was deleted by rejected cross-repo delete attempt: %v", err)
	}
}

// TestHandleDeploy_TarEntryEscapeRejected proves a tar.gz entry containing
// enough ".." segments to escape all the way past config.ServePath is
// rejected with 400, and that no file is ever written outside the deploy
// target -- the tar-entry side of the path containment vulnerability.
func TestHandleDeploy_TarEntryEscapeRejected(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)

	// dest for owner2/repo2/__ROOT__ is 4 levels below servePath's parent:
	// <parent>/<servePath>/owner2/repo2/__ROOT__ -- so 4 levels of ".." walks
	// all the way out of servePath entirely.
	body := makeTarGz(t, []string{"good.txt", "../../../../escape.txt"}, map[string]string{
		"good.txt":               "fine",
		"../../../../escape.txt": "should never land on disk",
	})
	q := url.Values{}
	q.Set("repo", "owner2/repo2")

	w := httptest.NewRecorder()
	handleDeploy(w, deployRequest(q, body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for tar path traversal, got %d: %s", w.Code, w.Body.String())
	}

	escapedPath := filepath.Join(filepath.Dir(servePath), "escape.txt")
	if _, err := os.Stat(escapedPath); !os.IsNotExist(err) {
		t.Fatalf("tar entry escaped containment: found file at %s (err=%v)", escapedPath, err)
	}

	// nothing should have been left behind inside servePath either: a failed
	// extraction discards its sibling staging directory and never creates the
	// live target, so no owner2/repo2/__ROOT__ content should exist.
	remaining, _ := filepath.Glob(filepath.Join(servePath, "owner2", "repo2", "__ROOT__", "*"))
	if len(remaining) != 0 {
		t.Fatalf("expected no live target content after failed deploy, found: %v", remaining)
	}
}

// TestHandleDeploy_TarEntrySiblingPrefixBypassRejected proves that a tar
// entry crafted to land in a *sibling* directory -- one whose name is the
// target directory's basename with a suffix appended (e.g. "__ROOT__evil"
// next to "__ROOT__") -- is rejected. A naive containment check built on
// strings.HasPrefix(resolvedPath, dest) instead of a real filepath.Rel
// check would have let this through: the string
// ".../owner3/repo3/__ROOT__evil/x.txt" has ".../owner3/repo3/__ROOT__" as
// a string prefix even though it resolves to a completely different,
// sibling directory outside dest.
func TestHandleDeploy_TarEntrySiblingPrefixBypassRejected(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)

	// dest for owner3/repo3 is <servePath>/owner3/repo3/__ROOT__. The entry
	// below resolves (lexically) to <servePath>/owner3/repo3/__ROOT__evil/x.txt,
	// i.e. one level up from dest and back down into a same-prefixed sibling.
	body := makeTarGz(t, []string{"good.txt", "../__ROOT__evil/x.txt"}, map[string]string{
		"good.txt":              "fine",
		"../__ROOT__evil/x.txt": "should never land on disk",
	})
	q := url.Values{}
	q.Set("repo", "owner3/repo3")

	w := httptest.NewRecorder()
	handleDeploy(w, deployRequest(q, body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for sibling-prefix tar path traversal, got %d: %s", w.Code, w.Body.String())
	}

	siblingPath := filepath.Join(servePath, "owner3", "repo3", "__ROOT__evil", "x.txt")
	if _, err := os.Stat(siblingPath); !os.IsNotExist(err) {
		t.Fatalf("tar entry escaped containment via sibling-prefix bypass: found file at %s (err=%v)", siblingPath, err)
	}

	remaining, _ := filepath.Glob(filepath.Join(servePath, "owner3", "repo3", "__ROOT__", "*"))
	if len(remaining) != 0 {
		t.Fatalf("expected target directory to be purged after failed deploy, found: %v", remaining)
	}

	assertNoStagingOrOldDirsRemain(t, filepath.Join(servePath, "owner3", "repo3"))
}

// TestHandleDeploy_FailedLimitMidExtraction_PreservesPreviousDeployment
// proves that a deploy which violates a limit partway through extraction must
// not touch the currently-live deployment at all: extraction lands in a
// sibling staging directory and only gets swapped into place after it fully
// succeeds, so an authorized but oversized/malformed upload never destroys
// the last-good site.
func TestHandleDeploy_FailedLimitMidExtraction_PreservesPreviousDeployment(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)

	q := url.Values{}
	q.Set("repo", "owner4/repo4")

	// first, a legitimate deployment that must survive the attack below
	goodBody := makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": "<h1>good</h1>"})
	w := httptest.NewRecorder()
	handleDeploy(w, deployRequest(q, goodBody))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for initial deploy, got %d: %s", w.Code, w.Body.String())
	}
	deployedFile := filepath.Join(servePath, "owner4", "repo4", "__ROOT__", "index.html")
	got, err := os.ReadFile(deployedFile)
	if err != nil || string(got) != "<h1>good</h1>" {
		t.Fatalf("initial deployment did not land as expected: content=%q err=%v", got, err)
	}

	// second deploy: the first entry extracts fine, the second violates the
	// per-entry size limit mid-extraction and must abort the whole deploy
	oversized := strings.Repeat("A", maxTarEntrySizeBytes+1)
	badBody := makeTarGz(t, []string{"first.txt", "oversized.bin"}, map[string]string{
		"first.txt":     "fine on its own",
		"oversized.bin": oversized,
	})
	w2 := httptest.NewRecorder()
	handleDeploy(w2, deployRequest(q, badBody))
	if w2.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized entry, got %d: %s", w2.Code, w2.Body.String())
	}

	// the previous deployment must still be intact and served
	got, err = os.ReadFile(deployedFile)
	if err != nil {
		t.Fatalf("previous deployment was destroyed by the rejected deploy: %v", err)
	}
	if string(got) != "<h1>good</h1>" {
		t.Fatalf("previous deployment content changed: got %q, want %q", got, "<h1>good</h1>")
	}

	// none of the failed upload's entries may have leaked into the live target
	if _, err := os.Stat(filepath.Join(servePath, "owner4", "repo4", "__ROOT__", "first.txt")); !os.IsNotExist(err) {
		t.Fatalf("partial extraction from the failed deploy leaked into the live target")
	}

	assertNoStagingOrOldDirsRemain(t, filepath.Join(servePath, "owner4", "repo4"))
}

// TestHandleDeploy_ConcurrentDeploysToSameTarget_NoCorruption fires several
// concurrent deploys at the same (owner, repo) target and asserts the final
// on-disk state is exactly one of the deployed payloads -- never a mix of
// two (which would indicate the extract-then-swap sequences interleaved)
// and never a panic/corrupt state. Run with `go test -race` to also catch
// any data race in the keyed-mutex serialization itself.
func TestHandleDeploy_ConcurrentDeploysToSameTarget_NoCorruption(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)

	const numConcurrent = 8
	q := url.Values{}
	q.Set("repo", "owner5/repo5")

	bodies := make([][]byte, numConcurrent)
	contents := make([]string, numConcurrent)
	for i := 0; i < numConcurrent; i++ {
		content := fmt.Sprintf("content-%d", i)
		contents[i] = content
		bodies[i] = makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": content})
	}

	var wg sync.WaitGroup
	codes := make([]int, numConcurrent)
	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			handleDeploy(w, deployRequest(q, bodies[i]))
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("concurrent deploy %d: expected 200, got %d", i, code)
		}
	}

	deployedFile := filepath.Join(servePath, "owner5", "repo5", "__ROOT__", "index.html")
	got, err := os.ReadFile(deployedFile)
	if err != nil {
		t.Fatalf("final deployed file missing/unreadable: %v", err)
	}
	finalContent := string(got)
	matched := false
	for _, c := range contents {
		if finalContent == c {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("final deployment content %q does not exactly match any single concurrent deploy's payload (corruption/interleaving)", finalContent)
	}

	assertNoStagingOrOldDirsRemain(t, filepath.Join(servePath, "owner5", "repo5"))
}

// TestHandleDeploy_SweepsStaleSiblingsAfterDeploy proves that pre-existing
// dot-prefixed ".<base>.old-*" and ".<base>.staging-*" sibling directories --
// left behind by, e.g., a previous promoteStaging call whose best-effort
// backup removal failed, or a process that died mid-deploy before its own
// staging cleanup ran -- are swept away once the next deploy to the same
// target succeeds, so these directories don't accumulate on disk forever.
// The sweep runs only after a successful promotion (never before), so it can
// never destroy a ".old-*" recovery copy while a replacement is still unproven.
func TestHandleDeploy_SweepsStaleSiblingsAfterDeploy(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)

	q := url.Values{}
	q.Set("repo", "owner6/repo6")

	// first deploy creates the target directory
	body := makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": "<h1>hi</h1>"})
	w := httptest.NewRecorder()
	handleDeploy(w, deployRequest(q, body))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for initial deploy, got %d: %s", w.Code, w.Body.String())
	}

	ownerRepoDir := filepath.Join(servePath, "owner6", "repo6")
	target := filepath.Join(ownerRepoDir, "__ROOT__")

	// simulate a stale ".old-*" backup left behind by a previous deploy that
	// died between the swap succeeding and the best-effort backup removal.
	// The name is dot-prefixed, matching the temp names this package generates.
	staleOld := filepath.Join(ownerRepoDir, ".__ROOT__.old-deadbeefdeadbeef")
	if err := os.MkdirAll(staleOld, 0750); err != nil {
		t.Fatalf("failed to seed stale .old-* dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleOld, "index.html"), []byte("<h1>stale</h1>"), 0640); err != nil {
		t.Fatalf("failed to seed stale .old-* content: %v", err)
	}

	// and a stale ".staging-*" dir, to prove both kinds get swept
	staleStaging := filepath.Join(ownerRepoDir, ".__ROOT__.staging-deadbeefdeadbeef")
	if err := os.MkdirAll(staleStaging, 0750); err != nil {
		t.Fatalf("failed to seed stale .staging-* dir: %v", err)
	}

	// second deploy should sweep both stale siblings and land cleanly
	body2 := makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": "<h1>new</h1>"})
	w2 := httptest.NewRecorder()
	handleDeploy(w2, deployRequest(q, body2))
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 for second deploy, got %d: %s", w2.Code, w2.Body.String())
	}

	got, err := os.ReadFile(filepath.Join(target, "index.html"))
	if err != nil || string(got) != "<h1>new</h1>" {
		t.Fatalf("second deployment did not land as expected: content=%q err=%v", got, err)
	}

	if _, err := os.Stat(staleOld); !os.IsNotExist(err) {
		t.Fatalf("stale .old-* sibling was not swept: %s still exists (err=%v)", staleOld, err)
	}
	if _, err := os.Stat(staleStaging); !os.IsNotExist(err) {
		t.Fatalf("stale .staging-* sibling was not swept: %s still exists (err=%v)", staleStaging, err)
	}

	assertNoStagingOrOldDirsRemain(t, ownerRepoDir)
}

// TestSafeURLForLogging_FailsClosedOnUnparseableQuery proves the redactor
// fails closed: for a query that url.ParseQuery cannot cleanly parse (a raw
// semicolon separator, or a bad percent-escape), a naive per-key redaction
// built on u.Query() would silently drop the offending key and return the URL
// with the secret still in it. safeURLForLogging must instead redact the
// whole query so the token value never reaches the logs.
func TestSafeURLForLogging_FailsClosedOnUnparseableQuery(t *testing.T) {
	cases := []struct {
		name   string
		rawURL string
		secret string
	}{
		{"raw semicolon separator", "http://pages.example.com/deploy?access_token=SECRET;x=1", "SECRET"},
		{"bad percent-escape", "http://pages.example.com/deploy?access_token=SECRET&x=%zz", "SECRET"},
		{"bad percent-escape in token key", "http://pages.example.com/deploy?token=SECRET%zz", "SECRET"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.rawURL)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", tc.rawURL, err)
			}
			got := safeURLForLogging(u)
			if strings.Contains(got, tc.secret) {
				t.Fatalf("safeURLForLogging leaked the secret: %q", got)
			}
			if !strings.Contains(got, redactedPlaceholder) {
				t.Fatalf("expected redacted placeholder in output, got: %q", got)
			}
		})
	}
}

// TestSafeURLForLogging_RedactsPerKeyOnParseableQuery proves the normal path
// still redacts each sensitive key individually (leaving other params intact)
// when the query parses cleanly.
func TestSafeURLForLogging_RedactsPerKeyOnParseableQuery(t *testing.T) {
	u, err := url.Parse("http://pages.example.com/deploy?repo=o/r&access_token=SECRET")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	got := safeURLForLogging(u)
	if strings.Contains(got, "SECRET") {
		t.Fatalf("safeURLForLogging leaked the secret: %q", got)
	}
	if !strings.Contains(got, "access_token=REDACTED") {
		t.Fatalf("expected access_token=REDACTED, got: %q", got)
	}
	if !strings.Contains(got, "repo=o%2Fr") && !strings.Contains(got, "repo=o/r") {
		t.Fatalf("expected non-sensitive repo param to be preserved, got: %q", got)
	}
}

// TestHandleDeploy_ProtectFlagCreationFailure_DoesNotPromote proves the
// protection step fails closed: if the ".protect" marker cannot be written to
// the staged deployment, the handler aborts promotion and the previous
// deployment stays live. The failure is forced deterministically by shipping
// a ".protect" *directory* entry in the archive, so os.Create of the marker
// file fails with EISDIR.
func TestHandleDeploy_ProtectFlagCreationFailure_DoesNotPromote(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)

	q := url.Values{}
	q.Set("repo", "owner11/repo11")

	// first, a legitimate deployment that must survive the failing deploy
	goodBody := makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": "<h1>good</h1>"})
	w := httptest.NewRecorder()
	handleDeploy(w, deployRequest(q, goodBody))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for initial deploy, got %d: %s", w.Code, w.Body.String())
	}
	deployedFile := filepath.Join(servePath, "owner11", "repo11", "__ROOT__", "index.html")

	// build an archive whose ".protect" entry is a directory, so the marker
	// file cannot be created over it
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "index.html", Mode: 0644, Size: int64(len("<h1>new</h1>"))}); err != nil {
		t.Fatalf("tar WriteHeader(index.html): %v", err)
	}
	if _, err := tw.Write([]byte("<h1>new</h1>")); err != nil {
		t.Fatalf("tar Write(index.html): %v", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: ".protect/", Mode: 0755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatalf("tar WriteHeader(.protect/): %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}

	q.Set("protect", "")
	w2 := httptest.NewRecorder()
	handleDeploy(w2, deployRequest(q, buf.Bytes()))
	if w2.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when .protect cannot be created, got %d: %s", w2.Code, w2.Body.String())
	}

	// the previous deployment must still be live and unchanged
	got, err := os.ReadFile(deployedFile)
	if err != nil {
		t.Fatalf("previous deployment was destroyed by the failed protect deploy: %v", err)
	}
	if string(got) != "<h1>good</h1>" {
		t.Fatalf("previous deployment content changed: got %q, want %q", got, "<h1>good</h1>")
	}

	assertNoStagingOrOldDirsRemain(t, filepath.Join(servePath, "owner11", "repo11"))
}

// TestHandleDeploy_UserBasePathLikeOldSuffix_NotSweptByNeighbourDeploy proves
// that a legitimate additional-base-path deployment whose name collides with
// the old temp-sibling glob shape (e.g. "preview.old-release") is NOT deleted
// by a later deploy to a neighbouring base path ("preview"). The temp names
// are dot-prefixed (".preview.old-*") and the sweep only matches those, so a
// user's own directory -- which can never start with "." -- is safe.
func TestHandleDeploy_UserBasePathLikeOldSuffix_NotSweptByNeighbourDeploy(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)

	// deploy a legitimate page under additional_base_path "preview.old-release"
	victim := url.Values{}
	victim.Set("repo", "owner9/repo9")
	victim.Set("additional_base_path", "preview.old-release")
	body := makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": "<h1>keep me</h1>"})
	w := httptest.NewRecorder()
	handleDeploy(w, deployRequest(victim, body))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for preview.old-release deploy, got %d: %s", w.Code, w.Body.String())
	}
	victimFile := filepath.Join(servePath, "owner9", "repo9", "preview.old-release", "index.html")
	if _, err := os.Stat(victimFile); err != nil {
		t.Fatalf("preview.old-release deployment did not land: %v", err)
	}

	// now deploy to the neighbouring base path "preview"; its stale-sibling
	// sweep must not touch "preview.old-release"
	neighbour := url.Values{}
	neighbour.Set("repo", "owner9/repo9")
	neighbour.Set("additional_base_path", "preview")
	body2 := makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": "<h1>neighbour</h1>"})
	w2 := httptest.NewRecorder()
	handleDeploy(w2, deployRequest(neighbour, body2))
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 for preview deploy, got %d: %s", w2.Code, w2.Body.String())
	}

	got, err := os.ReadFile(victimFile)
	if err != nil {
		t.Fatalf("preview.old-release deployment was swept by the preview deploy: %v", err)
	}
	if string(got) != "<h1>keep me</h1>" {
		t.Fatalf("preview.old-release content changed: got %q", got)
	}
}

// TestHandleDeploy_FailedDeploy_PreservesOldRecoveryCopy proves the
// crash-recovery invariant: when the live target is absent but a ".old-*"
// recovery copy exists (left by a previous failed swap+restore), a new deploy
// that itself fails (oversized archive) must leave that ".old-*" copy intact.
// The stale-sibling sweep now runs only AFTER a successful promotion, so a
// failed deploy never destroys the last known-good copy.
func TestHandleDeploy_FailedDeploy_PreservesOldRecoveryCopy(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)

	ownerRepoDir := filepath.Join(servePath, "owner10", "repo10")
	if err := os.MkdirAll(ownerRepoDir, 0750); err != nil {
		t.Fatalf("failed to create owner/repo dir: %v", err)
	}

	// seed a ".old-*" recovery copy, with the live target deliberately ABSENT
	// (as after a failed swap+restore left only the backup).
	recovery := filepath.Join(ownerRepoDir, ".__ROOT__.old-cafebabecafebabe")
	if err := os.MkdirAll(recovery, 0750); err != nil {
		t.Fatalf("failed to seed .old-* recovery dir: %v", err)
	}
	const recoveryContent = "<h1>last known good</h1>"
	if err := os.WriteFile(filepath.Join(recovery, "index.html"), []byte(recoveryContent), 0640); err != nil {
		t.Fatalf("failed to seed .old-* recovery content: %v", err)
	}

	// a new deploy that fails: an oversized entry aborts extraction
	q := url.Values{}
	q.Set("repo", "owner10/repo10")
	oversized := strings.Repeat("A", maxTarEntrySizeBytes+1)
	badBody := makeTarGz(t, []string{"oversized.bin"}, map[string]string{"oversized.bin": oversized})
	w := httptest.NewRecorder()
	handleDeploy(w, deployRequest(q, badBody))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized deploy, got %d: %s", w.Code, w.Body.String())
	}

	// the recovery copy must survive the failed deploy
	got, err := os.ReadFile(filepath.Join(recovery, "index.html"))
	if err != nil {
		t.Fatalf("the .old-* recovery copy was destroyed by the failed deploy: %v", err)
	}
	if string(got) != recoveryContent {
		t.Fatalf("recovery copy content changed: got %q, want %q", got, recoveryContent)
	}

	// and the failed deploy's own staging directory must have been cleaned up
	staging, _ := filepath.Glob(filepath.Join(ownerRepoDir, ".__ROOT__.staging-*"))
	if len(staging) != 0 {
		t.Fatalf("failed deploy left staging directories behind: %v", staging)
	}
}

// TestHandleDeploy_CumulativeExtractedSizeExceeded_PreservesPreviousDeployment
// proves that a deploy where every individual entry stays under the per-file
// limit (5 MiB) and the archive stays under the file-count limit (2500) can
// still be rejected once the *cumulative* uncompressed total exceeds the
// configured ceiling. It overrides config.MaxTotalExtractedBytes with a small
// value rather than the 256 MiB default so CI never has to write hundreds of
// megabytes, while still exercising the cumulative guard end-to-end through
// the handler.
func TestHandleDeploy_CumulativeExtractedSizeExceeded_PreservesPreviousDeployment(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)
	const ceiling = 10_000_000 // 10 MB configured ceiling, for a fast test
	config.MaxTotalExtractedBytes = ceiling

	q := url.Values{}
	q.Set("repo", "owner7/repo7")

	// first, a legitimate deployment that must survive the attack below
	goodBody := makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": "<h1>good</h1>"})
	w := httptest.NewRecorder()
	handleDeploy(w, deployRequest(q, goodBody))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for initial deploy, got %d: %s", w.Code, w.Body.String())
	}
	deployedFile := filepath.Join(servePath, "owner7", "repo7", "__ROOT__", "index.html")
	got, err := os.ReadFile(deployedFile)
	if err != nil || string(got) != "<h1>good</h1>" {
		t.Fatalf("initial deployment did not land as expected: content=%q err=%v", got, err)
	}

	// second deploy: 3 files x 5,000,000 bytes (15 MB total), each
	// individually well under the 5,242,880-byte (5 MiB) per-file limit, so
	// only the cumulative guard -- against the 10 MB configured ceiling --
	// can be what rejects this, not the pre-existing per-file check.
	const numFiles = 3
	const fileSize = 5_000_000
	content := strings.Repeat("A", fileSize) // one shared 5 MB string, reused by every entry below
	names := make([]string, numFiles)
	files := make(map[string]string, numFiles)
	for i := 0; i < numFiles; i++ {
		name := fmt.Sprintf("file-%02d.bin", i)
		names[i] = name
		files[name] = content
	}
	badBody := makeTarGz(t, names, files)

	w2 := httptest.NewRecorder()
	handleDeploy(w2, deployRequest(q, badBody))
	if w2.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for cumulative size over the configured ceiling, got %d: %s", w2.Code, w2.Body.String())
	}

	// the previous deployment must still be intact and served
	got, err = os.ReadFile(deployedFile)
	if err != nil {
		t.Fatalf("previous deployment was destroyed by the rejected deploy: %v", err)
	}
	if string(got) != "<h1>good</h1>" {
		t.Fatalf("previous deployment content changed: got %q, want %q", got, "<h1>good</h1>")
	}

	// none of the failed upload's entries may have leaked into the live target
	if _, err := os.Stat(filepath.Join(servePath, "owner7", "repo7", "__ROOT__", "file-00.bin")); !os.IsNotExist(err) {
		t.Fatalf("partial extraction from the failed deploy leaked into the live target")
	}

	assertNoStagingOrOldDirsRemain(t, filepath.Join(servePath, "owner7", "repo7"))
}

// TestHandleDeploy_CumulativeExtractedSize_UnderConfiguredCeiling_Succeeds is
// the positive control for the cumulative guard: a deployment whose
// cumulative uncompressed total lands just under the configured ceiling must
// still succeed. It overrides config.MaxTotalExtractedBytes with a small
// custom value (rather than relying on the 256 MiB default) so the test stays
// fast while still proving the ceiling is actually read from config.
func TestHandleDeploy_CumulativeExtractedSize_UnderConfiguredCeiling_Succeeds(t *testing.T) {
	forge := stubForgejoWritable(t)
	setupDeployTestConfig(t, forge.URL)
	config.MaxTotalExtractedBytes = 10_000_000 // 10 MB custom ceiling

	q := url.Values{}
	q.Set("repo", "owner8/repo8")

	// two files summing to just under the configured 10 MB ceiling, each
	// individually well under the 5 MiB per-file limit
	names := []string{"a.bin", "b.bin"}
	files := map[string]string{
		"a.bin": strings.Repeat("A", 4_999_998),
		"b.bin": strings.Repeat("B", 4_999_998), // total 9,999,996 bytes, just under 10,000,000
	}
	body := makeTarGz(t, names, files)

	w := httptest.NewRecorder()
	handleDeploy(w, deployRequest(q, body))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for deployment just under the configured ceiling, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleDeploy_BearerTokenHeaderOnly_Accepted proves that a deploy
// request presenting the token exclusively via "Authorization: Bearer
// <token>", with no access_token/token in the query string at all, is
// accepted end-to-end through the real permission-check path
// (ForgeCheckRepoWritableWithAccessToken against the stub Forgejo): the
// server no longer requires, or even reads, a query-string token.
func TestHandleDeploy_BearerTokenHeaderOnly_Accepted(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)

	body := makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": "<h1>hi</h1>"})
	q := url.Values{}
	q.Set("repo", "ownerbearer/repobearer")
	// deliberately no access_token/token in the query -- deployRequest sets
	// the Authorization header instead.

	req := deployRequest(q, body)
	if got := req.URL.Query().Get("access_token"); got != "" {
		t.Fatalf("test setup error: query unexpectedly contains access_token=%q", got)
	}

	w := httptest.NewRecorder()
	handleDeploy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for header-only bearer token, got %d: %s", w.Code, w.Body.String())
	}
	deployedFile := filepath.Join(servePath, "ownerbearer", "repobearer", "__ROOT__", "index.html")
	if _, err := os.Stat(deployedFile); err != nil {
		t.Fatalf("expected deployed file at %s: %v", deployedFile, err)
	}
}

// TestHandleDeploy_QueryTokenOnly_Rejected proves that a deploy request
// carrying the deploy token only via the legacy "?access_token=" query
// parameter -- with no Authorization header at all -- is rejected with a
// 410 Gone response that tells the caller to switch to the header. This is
// the core fix for the token-in-URL leak: the token must never be accepted
// from the query string, only from "Authorization: Bearer".
func TestHandleDeploy_QueryTokenOnly_Rejected(t *testing.T) {
	forge := stubForgejoWritable(t)
	setupDeployTestConfig(t, forge.URL)

	body := makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": "<h1>hi</h1>"})
	q := url.Values{}
	q.Set("repo", "ownerquery/repoquery")
	q.Set("access_token", "faketoken")

	target := "http://pages.example.com/deploy?" + q.Encode()
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	req.Host = "pages.example.com"
	// deliberately no Authorization header

	w := httptest.NewRecorder()
	handleDeploy(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("expected 410 Gone for query-only token, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(strings.ToLower(w.Body.String()), "authorization") {
		t.Fatalf("expected rejection message to mention the Authorization header, got: %s", w.Body.String())
	}
}

// TestHandleDelete_QueryTokenOnly_Rejected is the delete-side counterpart
// of TestHandleDeploy_QueryTokenOnly_Rejected: DELETE /deploy must apply
// the exact same Authorization-header-only token contract as POST /deploy.
func TestHandleDelete_QueryTokenOnly_Rejected(t *testing.T) {
	forge := stubForgejoWritable(t)
	setupDeployTestConfig(t, forge.URL)

	q := url.Values{}
	q.Set("repo", "ownerdelquery/repodelquery")
	q.Set("access_token", "faketoken")

	target := "http://pages.example.com/deploy?" + q.Encode()
	req := httptest.NewRequest(http.MethodDelete, target, nil)
	req.Host = "pages.example.com"
	// deliberately no Authorization header

	w := httptest.NewRecorder()
	handleDeploy(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("expected 410 Gone for query-only token on delete, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(strings.ToLower(w.Body.String()), "authorization") {
		t.Fatalf("expected rejection message to mention the Authorization header, got: %s", w.Body.String())
	}
}

// TestHandleDelete_HeaderPlusQueryToken_Rejected is the delete-side
// counterpart: DELETE /deploy must also reject a request that carries a query
// token even when a valid Authorization header is present, and must not
// delete anything.
func TestHandleDelete_HeaderPlusQueryToken_Rejected(t *testing.T) {
	forge := stubForgejoWritable(t)
	servePath := setupDeployTestConfig(t, forge.URL)

	// seed a deployment that must survive the rejected delete
	pageDir := filepath.Join(servePath, "ownerdelhq", "repodelhq", "__ROOT__")
	if err := os.MkdirAll(pageDir, 0750); err != nil {
		t.Fatalf("failed to seed page dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pageDir, "index.html"), []byte("keep"), 0640); err != nil {
		t.Fatalf("failed to seed page file: %v", err)
	}

	q := url.Values{}
	q.Set("repo", "ownerdelhq/repodelhq")
	q.Set("access_token", "faketoken")

	// deleteRequest sets the Authorization header; the query token rides along
	req := deleteRequest(q)

	w := httptest.NewRecorder()
	handleDeploy(w, req)
	if w.Code != http.StatusGone {
		t.Fatalf("expected 410 Gone for header+query token on delete, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(pageDir, "index.html")); err != nil {
		t.Fatalf("deployment was deleted by a rejected delete: %v", err)
	}
}

// TestHandleDeploy_HeaderPlusQueryToken_Rejected proves that a request
// carrying a legacy "?access_token=SECRETVALUE" query parameter is rejected
// with 410 Gone even when it also presents a valid Authorization header: a
// token that traveled in the URL can never authorize a request. It also
// proves the redaction half of the guarantee -- the incoming-request log
// (emitted before the rejection) never contains the token value, only the
// query with it redacted.
func TestHandleDeploy_HeaderPlusQueryToken_Rejected(t *testing.T) {
	forge := stubForgejoWritable(t)
	setupDeployTestConfig(t, forge.URL)

	body := makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": "<h1>hi</h1>"})
	q := url.Values{}
	q.Set("repo", "ownerlog/repolog")
	q.Set("access_token", "SECRETVALUE")

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOutput)
		log.SetFlags(prevFlags)
	})

	// deployRequest sets the Authorization header; the stray query
	// access_token rides along as an old client's leftover would. Even so,
	// the presence of the query token must get the request rejected.
	req := deployRequest(q, body)

	w := httptest.NewRecorder()
	handleDeploy(w, req)
	if w.Code != http.StatusGone {
		t.Fatalf("expected 410 Gone for header+query token, got %d: %s", w.Code, w.Body.String())
	}

	logs := logBuf.String()
	if strings.Contains(logs, "SECRETVALUE") {
		t.Fatalf("log output leaked the access_token value: %q", logs)
	}
	if !strings.Contains(logs, "/deploy") {
		t.Fatalf("expected log output to contain the request path, got: %q", logs)
	}
	if !strings.Contains(logs, "access_token=REDACTED") {
		t.Fatalf("expected log output to contain the redacted access_token marker, got: %q", logs)
	}
}

// TestHandleDeploy_LogsRedactAccessToken_RepeatedWithEmptyFirst tests that
// even when a sensitive query param has multiple values and the first value
// is empty (e.g., "?access_token=&access_token=SECRETVALUE2"), ALL values are
// redacted, not just the (checked-but-empty) first one. This is a regression
// test for the vulnerability where q.Get(key) != "" would skip redaction if
// the first value was empty, leaving the second value exposed in logs.
func TestHandleDeploy_LogsRedactAccessToken_RepeatedWithEmptyFirst(t *testing.T) {
	forge := stubForgejoWritable(t)
	setupDeployTestConfig(t, forge.URL)

	body := makeTarGz(t, []string{"index.html"}, map[string]string{"index.html": "<h1>hi</h1>"})

	// Construct the URL manually to include multiple access_token values
	// where the first is empty and the second holds a secret.
	target := "http://pages.example.com/deploy?repo=ownerrepeat/repolog&access_token=&access_token=SECRETVALUE2"
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	req.Host = "pages.example.com"
	req.Header.Set("Authorization", "Bearer faketoken")

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOutput)
		log.SetFlags(prevFlags)
	})

	w := httptest.NewRecorder()
	handleDeploy(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	logs := logBuf.String()
	if strings.Contains(logs, "SECRETVALUE2") {
		t.Fatalf("log output leaked the second access_token value: %q", logs)
	}
	if !strings.Contains(logs, "/deploy") {
		t.Fatalf("expected log output to contain the request path, got: %q", logs)
	}
	if !strings.Contains(logs, "access_token=REDACTED") {
		t.Fatalf("expected log output to contain the redacted access_token marker, got: %q", logs)
	}
}
