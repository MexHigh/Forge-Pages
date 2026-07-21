package main

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"code.leon.wtf/leon/forge-pages/pathsafe"
	"golang.org/x/oauth2"
)

// lookupRoute checks if the request host matches a configured custom route.
// If found, it returns the configured owner, repo, and true.
func lookupRoute(host string) (owner, repo string, found bool) {
	if config.Routes == nil {
		return "", "", false
	}
	route, ok := config.Routes[host]
	if !ok {
		return "", "", false
	}
	return route.Owner, route.Repo, true
}

func handlePage(w http.ResponseWriter, r *http.Request) {
	// check host
	baseHost := config.GetPagesURLHostOnly()
	if r.Host == baseHost { // request is exactly for http[s]://<page_url>/ --> fallback response
		log.Println("User hit base URL (/)")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello!"))
		return
	}

	if !strings.HasSuffix(r.Host, "."+baseHost) { // request is <owner>.<page_url>
		if owner, repo, ok := lookupRoute(r.Host); ok {
			log.Printf("Custom route matched: host=%s → owner=%s, repo=%s", r.Host, owner, repo)
			serveDeployedPage(w, r, owner, repo, "")
			return
		}
		log.Printf("User hit URL not ending in pages_url (Host: %s, path: %s)", r.Host, safeURLForLogging(r.URL))
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: host does not end in configured pages_url (host part only)"))
		return
	}

	// get and check repo owner -- shared accepted-host predicate with
	// getFullURL (see resolvePagesHostLabel) so the two can't drift.
	repoOwner, ok := resolvePagesHostLabel(r.Host, baseHost)

	// check custom routes by repoOwner subdomain before standard parsing
	if owner, repo, ok := lookupRoute(r.Host); ok {
		log.Printf("Custom route matched: host=%s → owner=%s, repo=%s", r.Host, owner, repo)
		serveDeployedPage(w, r, owner, repo, "")
		return
	}

	if strings.ContainsAny(repoOwner, ".,/") {
		log.Printf("User hit URL with malformed owner subdomain (Host: %s, path: %s)", r.Host, safeURLForLogging(r.URL))
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: repo owner (subdomain) is malformed"))
		return
	}

	// get path for repo or check custom route for root path
	urlHelper := NewURLPathHelper(r.URL.Path)
	if urlHelper.NumOfElements == 0 {
		// before returning 400, check if a custom route exists for this host
		if owner, repo, ok := lookupRoute(r.Host); ok {
			log.Printf("Custom route matched for root path: host=%s → owner=%s, repo=%s", r.Host, owner, repo)
			serveDeployedPage(w, r, owner, repo, "")
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: pages request sent to root folder"))
		return
	}
	repoPath := urlHelper.GetElement(0)

	// check if seperate base path exists
	addBasePath := ""
	if urlHelper.HasElement(1) {
		log.Println("Possible additional base path found, checking if it exists")
		tempPage, err := NewForgePage(repoOwner, repoPath, urlHelper.GetElement(1))
		if err != nil {
			// not a valid additional-base-path label (e.g. contains "..") --
			// it can never have been a legitimately deployed path, so just
			// fall back to __ROOT__ instead of erroring out the whole request
			log.Printf("Additional base path candidate rejected: %s", err.Error())
		} else if tempPage.Exists() {
			// this means that the addBasePath does not exist, not that the page does not exist in __ROOT__
			log.Println("Additional base path exists, requesting")
			addBasePath = urlHelper.GetElement(1)
		} else {
			log.Println("Not an additional base path, falling back to __ROOT__")
		}
	}

	// serve the page using the standard owner/repo from host+path
	serveDeployedPage(w, r, repoOwner, repoPath, addBasePath)
}

// serveDeployedPage serves content for a given owner/repo combo.
// For custom routes, the additional path after the host is treated as an asset sub-path.
func serveDeployedPage(w http.ResponseWriter, r *http.Request, owner, repo, addBasePath string) {
	// assemble page struct
	page, err := NewForgePage(owner, repo, addBasePath)
	if err != nil {
		log.Printf("Rejecting page request: %s", err.Error())
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "Bad request: %s", err.Error())
		return
	}

	if !page.Exists() {
		log.Printf("Requested path %s empty or does not exist (Host: %s, path: %s)", page.StoragePath, r.Host, safeURLForLogging(r.URL))
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "Not found: no page deployed for repo %s/%s", owner, repo)
		return
	}

	// check if oauth2 check is required
	if page.HasProtectionFlag() {
		log.Printf("Protected page hit (Host: %s, path: %s)", r.Host, safeURLForLogging(r.URL))

		// get access token or redirect to login if not found
		tokenIface := sessionManager.Get(r.Context(), "access_token")
		token, ok := tokenIface.(oauth2.Token)
		if !ok {
			log.Println("access_token not found in session")

			// set redirect target, then do redirect to oauth
			fullURL, ok := getFullURL(r)
			if !ok {
				// r.Host is not the configured pages_url host nor a valid
				// single-label subdomain of it. This branch should be
				// unreachable in practice (the host check at the top of
				// handlePage already rejected anything else), but
				// getFullURL re-validates independently -- it must never
				// build a redirect target from an untrusted host, even if
				// some future caller reaches it without going through that
				// earlier check first.
				log.Printf("Rejecting OAuth redirect target: invalid host (Host: %s, path: %s)", r.Host, safeURLForLogging(r.URL))
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("Bad request: invalid host"))
				return
			}
			sessionManager.Put(r.Context(), "redirect_to", fullURL)
			log.Printf("redirect_url set to %s, redirecting to /login", safeURLStringForLogging(fullURL))
			http.Redirect(w, r, config.PagesURL+"/login", http.StatusFound)
			return
		}

		// generate client from access token and check permissions
		client := oauthConf.Client(r.Context(), &token)

		log.Println("Checking permissions via API")
		readable, err := ForgeCheckRepoReadableWithClient(fmt.Sprintf("%s/%s", owner, repo), client)
		if err != nil {
			log.Printf("Error while checking permissions: %s", err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Server error: unable to check permissions on the target repository"))
			return
		}
		if !readable {
			log.Println("User is not allowed to read repository --> sending unauthorized")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("Unauthorized: no read access for you :("))
			return
		}

		log.Println("OAuth2 successful and user has read permissions, continue serving assets")
	}

	// deliver static content
	// For custom routes, the URL path is used as asset path within the configured repo
	urlHelper := NewURLPathHelper(r.URL.Path)
	//
	// The request-relative path is attacker-controlled (arbitrary URL path
	// segments, including "..") so it must be routed through
	// pathsafe.SecureJoin rather than a bare filepath.Join+Clean, which only
	// normalizes the path and relies on net/http's own (implicit, easy to
	// regress) ".." guards inside ServeFile for containment.
	startElem := 1
	if addBasePath != "" { // "if we have an existing addBasePath"
		startElem = 2
	}
	assetsPathRequest := page.StoragePath
	if urlHelper.HasElement(startElem) {
		// guard: NumOfElements < startElem (custom route root path) must be
		// rejected here -- GetElementsStartingFromElement would slice out of
		// range, and assetsPathRequest stays as page.StoragePath instead
		relAssetPath := filepath.Clean(urlHelper.GetElementsStartingFromElement(startElem))
		assetsPathRequest, err = pathsafe.SecureJoin(page.StoragePath, strings.Split(relAssetPath, "/")...)
		if err != nil {
			// treat an out-of-root asset path the same as a missing asset,
			// instead of leaking containment details to the client.
			log.Printf("Rejecting asset request outside page storage path: %s", err.Error())
			http.NotFound(w, r)
			return
		}
	}

	log.Println("Serving asset " + assetsPathRequest)
	http.ServeFile(w, r, assetsPathRequest)
}

// isValidPagesOwnerLabel reports whether label is safe to use as the owner
// subdomain component of a pages host ("<label>.<pages_url-host>").
//
// pathsafe.SafeLabel enforces the general path-safety subset (alnum-bounded,
// bounded length, rejects "", ".", ".."), but its charset also permits dots
// -- valid for a general path component, but not for a single DNS label.
// Without the extra dot check, a Host like "a.b.pages.example.com" would
// incorrectly validate: stripping the "."+baseHost suffix leaves "a.b",
// which SafeLabel alone would accept as if it were one label instead of
// being rejected as a malformed/multi-level subdomain.
func isValidPagesOwnerLabel(label string) bool {
	return pathsafe.SafeLabel(label) == nil && !strings.Contains(label, ".")
}

// resolvePagesHostLabel reports whether host is a valid single-label
// subdomain of baseHost -- the "<owner>.<baseHost>" shape every legitimate
// pages request uses -- and if so returns the owner label. It returns
// ok=false for a host that lacks the "."+baseHost suffix, or whose remaining
// label is not a valid pages owner label (see isValidPagesOwnerLabel). It
// deliberately does NOT accept host == baseHost (the bare base host carries
// no owner label); callers that treat the base host specially must check for
// it themselves.
//
// This is the single accepted-host predicate shared by handlePage and
// getFullURL, so the shape of hosts they accept cannot drift between them and
// silently reopen host-header injection in the OAuth redirect target.
func resolvePagesHostLabel(host, baseHost string) (label string, ok bool) {
	suffix := "." + baseHost
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	label = strings.TrimSuffix(host, suffix)
	if !isValidPagesOwnerLabel(label) {
		return "", false
	}
	return label, true
}

// getFullURL builds the externally visible URL for the incoming request,
// used as the OAuth2 post-login redirect target (stored in the session as
// "redirect_to" in handlePage, replayed by handleCallback in oauth2.go). It
// deliberately does not trust the request for scheme or host:
//
//   - scheme: r.TLS is nil for every request behind a TLS-terminating
//     reverse proxy (which terminates TLS and forwards plaintext to
//     this service), so deriving scheme from r.TLS silently downgrades
//     every OAuth return URL to "http://", which then mismatches the
//     "https://" redirect_uri registered with the OAuth provider and
//     breaks the login flow for any protected page. The scheme instead
//     comes from config.GetPagesURLScheme(), i.e. the operator-configured
//     pages_url, which is the actual public-facing scheme.
//   - host: r.Host is attacker-controlled (any HTTP client can send an
//     arbitrary Host header). Using it unvalidated would let a crafted
//     request redirect the OAuth flow to an arbitrary domain
//     (host-header injection / open redirect). The host is only accepted
//     if it is exactly the configured pages_url host, or a single
//     DNS-label subdomain of it (the "<owner>.<pages_url-host>" pattern
//     every legitimate page request uses -- see isValidPagesOwnerLabel).
//     Even then, the returned URL's host is rebuilt from the validated
//     base host + label, never copied from r.Host as-is.
//
// Only the request's path+query are taken from r as-is -- that is the
// actual post-login redirect target, not a scheme/host trust decision.
//
// ok is false when r.Host fails validation; callers must reject the
// request (400/404) instead of building a redirect from it.
func getFullURL(r *http.Request) (fullURL string, ok bool) {
	baseHost := config.GetPagesURLHostOnly()

	host := baseHost
	if r.Host != baseHost {
		label, hostOK := resolvePagesHostLabel(r.Host, baseHost)
		if !hostOK {
			return "", false
		}
		host = label + "." + baseHost
	}

	scheme := config.GetPagesURLScheme()
	return fmt.Sprintf("%s://%s%s", scheme, host, r.URL.String()), true
}
