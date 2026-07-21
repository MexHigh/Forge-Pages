package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// redactedQueryKeys lists query parameter names that must never appear
// verbatim in logs. The server no longer accepts a deploy token via the
// query string at all (see bearerToken), but this list stays as
// defense-in-depth: an old client, a bookmarked URL, or a probe that still
// sends "?access_token=..." must never have that value land in the logs.
var redactedQueryKeys = []string{"access_token", "token"}

// redactedPlaceholder replaces a redacted query value. Deliberately
// alphanumeric (not e.g. "***") so url.Values.Encode() round-trips it
// as-is instead of percent-encoding it, keeping the redaction obvious and
// grep-able in raw log output.
const redactedPlaceholder = "REDACTED"

// safeURLForLogging returns u's string form with any redactedQueryKeys
// values replaced by redactedPlaceholder, so callers can log a full request
// URL (including query, which is useful for debugging -- repo, protect,
// additional_base_path, etc) without risking a secret ending up in log
// output.
func safeURLForLogging(u *url.URL) string {
	if u == nil {
		return ""
	}
	// Parse the query ourselves rather than via u.Query(), which silently
	// discards a parse error: a query like "access_token=SECRET;x=1" (raw
	// semicolon) or one with a bad percent-escape makes u.Query() drop the
	// offending key, so a per-key redaction would return the URL with the
	// secret still in it. If the query cannot be cleanly parsed we cannot
	// trust any per-key redaction, so we fail closed and redact the whole
	// query.
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		redacted := *u
		redacted.RawQuery = redactedPlaceholder
		return redacted.String()
	}
	changed := false
	for _, key := range redactedQueryKeys {
		if _, present := q[key]; present {
			q.Set(key, redactedPlaceholder)
			changed = true
		}
	}
	if !changed {
		return u.String()
	}
	redacted := *u
	redacted.RawQuery = q.Encode()
	return redacted.String()
}

// safeURLStringForLogging is the string counterpart of safeURLForLogging, for
// log sites that hold a full URL as a string (e.g. the stored OAuth
// redirect target). If the string cannot be parsed as a URL it is not safe
// to log verbatim -- it might contain an unparseable query hiding a secret --
// so it fails closed and returns only the redacted placeholder.
func safeURLStringForLogging(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return redactedPlaceholder
	}
	return safeURLForLogging(u)
}

// errQueryTokenRejected is returned by bearerToken when a request has no
// Authorization header but does carry the (now-removed) access_token/token
// query parameter. It gets its own error so handlers can respond with a
// specific, actionable message instead of a generic "missing token" one.
var errQueryTokenRejected = errors.New(
	`the deploy token is no longer accepted as a query parameter; ` +
		`provide it via the "Authorization: Bearer <token>" header instead`)

// bearerToken extracts the deploy token from the Authorization header. The
// query-string "access_token"/"token" parameters are no longer accepted --
// the token must never travel in the URL, since URLs (and therefore the
// token) end up in server logs, reverse-proxy access logs, and browser/CI
// history.
//
// A legacy query token is rejected unconditionally, even when a valid
// Authorization header is also present: honoring the header while ignoring
// the query token would still let a request that carried a secret in its URL
// succeed, so any such request is refused outright and the caller is pointed
// at the header-only contract.
func bearerToken(r *http.Request) (string, error) {
	q := r.URL.Query()
	if q.Get("access_token") != "" || q.Get("token") != "" {
		return "", errQueryTokenRejected
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		token, ok := strings.CutPrefix(authHeader, "Bearer ")
		if !ok || token == "" {
			return "", errors.New(`malformed Authorization header, expected "Bearer <token>"`)
		}
		return token, nil
	}

	return "", errors.New("missing deploy token")
}

// deployToken returns the bearer token for a deploy/delete request. When it
// cannot, it writes the appropriate error response -- 410 Gone for a
// rejected legacy query token, 400 Bad Request otherwise -- and returns
// ok=false so the caller returns immediately. When deploy checks are
// skipped it returns an empty token and ok=true without inspecting the
// request.
func deployToken(w http.ResponseWriter, r *http.Request) (token string, ok bool) {
	if *skipDeployChecks {
		return "", true
	}
	token, err := bearerToken(r)
	if err != nil {
		if errors.Is(err, errQueryTokenRejected) {
			w.WriteHeader(http.StatusGone)
			w.Write([]byte("Gone: " + err.Error()))
		} else {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Bad request: " + err.Error()))
		}
		return "", false
	}
	return token, true
}

func handleDeploy(w http.ResponseWriter, r *http.Request) {
	log.Printf("Incoming request to /deploy (Host: %s, path: %s)", r.Host, safeURLForLogging(r.URL))

	switch r.Method {
	case http.MethodPost:
		if r.Host != config.GetPagesURLHostOnly() {
			log.Printf("Hit deploy route on non-root subdomain: %s", r.Host)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Bad request: use deploy route on configured root domain"))
			return
		}
		handleNewDeployment(w, r)
	case http.MethodDelete:
		handleDeleteDeployment(w, r)
	default:
		log.Printf("Method not allowed: %s", r.Method)
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("Method not allowed"))
	}
}

func handleNewDeployment(w http.ResponseWriter, r *http.Request) {
	// get params
	repo, addBasePath, protect, err := getParams(r.URL.Query())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: " + err.Error()))
		return
	}

	accessToken, ok := deployToken(w, r)
	if !ok {
		return
	}
	log.Printf("Someone is trying to deploy from repo %s, checking access", repo)

	// check write access
	writable := false
	if *skipDeployChecks {
		writable = true
	} else {
		writable, err = ForgeCheckRepoWritableWithAccessToken(repo, accessToken)
		if err != nil {
			log.Printf("Error while checking write permissions: %s", err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Server error: unable to check permissions on the target repository"))
			return
		}
		if !writable {
			log.Println("User does not have write permissions --> aborting")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("Unauthorized: user does not have write permissions on this repository"))
			return
		}
	}
	log.Println("User has write permissions")

	// check repo param syntax
	repoParts := strings.Split(repo, "/")
	if len(repoParts) != 2 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: repo does not have exactly one /"))
		return
	}
	forgePage, err := NewForgePage(repoParts[0], repoParts[1], addBasePath)
	if err != nil {
		log.Printf("Rejecting deploy: %s", err.Error())
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "Bad request: %s", err.Error())
		return
	}

	// Serialize deploy/delete operations against this exact target so
	// concurrent requests can't interleave their extract+swap sequences.
	unlock := lockDeployTarget(forgePage.StoragePath)
	defer unlock()

	// Extract into a sibling staging directory, never into the live target
	// directly: an authorized but oversized/malformed upload must not
	// destroy the last known-good deployment before a replacement is
	// proven good.
	log.Printf("Staging new deployment for %s", forgePage.StoragePath)
	stagingPath, err := createStagingDir(forgePage.StoragePath)
	if err != nil {
		log.Printf("Error creating staging directory: %s", err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Server error: could not create staging directory"))
		return
	}

	// The staging dir must never outlive this handler unless promotion
	// actually succeeds. Tracking success in `promoted` and cleaning up in
	// a defer (rather than only in each error branch) also covers a panic
	// during extraction/promotion, which per-branch RemoveAll calls cannot:
	// a panic skips straight past them and would otherwise orphan the
	// staging dir forever.
	promoted := false
	defer func() {
		if !promoted {
			if rmErr := os.RemoveAll(stagingPath); rmErr != nil {
				log.Printf("Error removing staging directory %s: %s", stagingPath, rmErr.Error())
			}
		}
	}()

	// read and extract tar.gz file into staging
	log.Println("Reading body")
	if err := extractTarGz(r.Body, stagingPath); err != nil {
		log.Printf("Error while reading .tar.gz body: %s; discarding staged deployment, previous deployment (if any) is untouched", err.Error())
		// filter some errors to send accurate codes
		switch {
		case errors.Is(err, ErrEntryTooLarge) || errors.Is(err, ErrTooManyEntries) || errors.Is(err, ErrTotalSizeExceeded):
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			w.Write([]byte("Server error: could not extract tar.gz"))
		case errors.Is(err, ErrPathTraversal):
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "Bad request: %s", err.Error())
		default:
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Server error: could not extract tar.gz"))
		}
		return
	}

	// Add the protect flag to the staged deployment BEFORE promotion (a
	// deployment can also carry the flag directly inside its tar.gz, which
	// still protects the page via HasProtectionFlag). This must fail closed:
	// if the flag can't be written we abort here, leaving the previous
	// deployment live rather than publishing an unprotected page.
	stagingPage := &ForgePage{StoragePath: stagingPath, Owner: forgePage.Owner, Repo: forgePage.Repo}
	if protect {
		log.Println("User requested protection, adding flag")
		if err := stagingPage.AddProtectionFlag(); err != nil {
			log.Printf("Error adding protection flag, aborting deploy: %s", err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Server error: could not add protection flag"))
			return
		}
	}

	// the staged deployment is fully validated and extracted -- swap it
	// into place atomically, preserving the previous deployment until the
	// very last moment in case the swap itself fails
	if err := promoteStaging(forgePage.StoragePath, stagingPath); err != nil {
		log.Printf("Error promoting staged deployment: %s", err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Server error: could not activate new deployment"))
		return
	}
	promoted = true

	// Now that the new deployment is live, best-effort sweep any stale
	// ".staging-*"/".old-*" sibling directories left behind by a previous,
	// interrupted handler invocation for this exact target (process killed,
	// panic recovered upstream, etc). This runs only AFTER a successful
	// promotion: doing it earlier could destroy a ".old-*" recovery copy that
	// a failed swap+restore deliberately left as the last known-good site.
	// Safe here because we still hold the per-target lock.
	sweepStaleSiblingDirs(forgePage.StoragePath)

	msg := fmt.Sprintf("Success, deployed to %s/%s", config.GetPagesURLWithAdditionalSubdomain(forgePage.Owner), forgePage.Repo)
	if addBasePath != "" {
		msg += "/" + addBasePath
	}
	if forgePage.HasProtectionFlag() {
		msg += " (protected, requires OAuth2 authentication)"
	}
	log.Println(msg)
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, msg)
}

func handleDeleteDeployment(w http.ResponseWriter, r *http.Request) {
	// get params
	repo, addBasePath, _, err := getParams(r.URL.Query())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: " + err.Error()))
		return
	}

	accessToken, ok := deployToken(w, r)
	if !ok {
		return
	}
	log.Printf("Someone is trying to delete page for repo %s, checking access", repo)

	// check repo param syntax
	repoParts := strings.Split(repo, "/")
	if len(repoParts) != 2 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: repo does not have exactly one /"))
		return
	}
	// check username valid
	if err := checkUsernameValid(repoParts[0]); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "Bad request: %s", err.Error())
		return
	}

	// check write access
	writable := false
	if *skipDeployChecks {
		writable = true
	} else {
		writable, err = ForgeCheckRepoWritableWithAccessToken(repo, accessToken)
		if err != nil {
			log.Printf("Error while checking write permissions: %s", err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Server error: unable to check permissions on the target repository"))
			return
		}
		if !writable {
			log.Println("User does not have write permissions --> aborting")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("Unauthorized: user does not have write permissions on this repository"))
			return
		}
	}

	// do the delete
	forgePage, err := NewForgePage(repoParts[0], repoParts[1], addBasePath)
	if err != nil {
		log.Printf("Rejecting delete: %s", err.Error())
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "Bad request: %s", err.Error())
		return
	}

	// Serialize against any in-flight deploy/delete for this exact target.
	unlock := lockDeployTarget(forgePage.StoragePath)
	defer unlock()

	log.Printf("Deleting page path %s", forgePage.StoragePath)
	if err := forgePage.Purge(); err != nil {
		log.Printf("Error deleting deployment %s", err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Server error: could not delete page directory"))
		return
	}

	msg := fmt.Sprintf("Success, deleted %s/%s", config.GetPagesURLWithAdditionalSubdomain(forgePage.Owner), forgePage.Repo)
	if addBasePath != "" {
		msg += "/" + addBasePath
	}
	log.Println(msg)
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, msg)
}

func getParams(urlQuery url.Values) (repo, addBasePath string, protect bool, err error) {
	repo = urlQuery.Get("repo")
	if repo == "" {
		err = errors.New("missing parameter: repo")
		return
	}
	addBasePath = urlQuery.Get("additional_base_path") // if not set --> empty string
	protect = urlQuery.Has("protect")
	return
}

func checkUsernameValid(username string) error {
	if usernameContainsDot(username) {
		return errors.New("username contains a dot, which is not supported")
	}
	return nil
}

func usernameContainsDot(username string) bool {
	return strings.Contains(username, ".")
}
