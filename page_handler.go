package main

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

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
	if r.Host == config.GetPagesURLHostOnly() { // request is exactly for http[s]://<page_url>/ --> fallback response
		log.Println("User hit base URL (/)")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello!"))
		return
	}
	if !strings.HasSuffix(r.Host, "."+config.GetPagesURLHostOnly()) { // request is <owner>.<page_url>
		// check if this is a custom route before rejecting
		if owner, repo, ok := lookupRoute(r.Host); ok {
			log.Printf("Custom route matched: host=%s → owner=%s, repo=%s", r.Host, owner, repo)
			serveDeployedPage(w, r, owner, repo, "")
			return
		}
		log.Printf("User hit URL not ending in pages_url (Host: %s, path: %s)", r.Host, r.URL)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: host does not end in configured pages_url (host part only)"))
		return
	}

	// get and check repo owner
	repoOwner := strings.TrimSuffix(r.Host, "."+config.GetPagesURLHostOnly())

	// check custom routes by repoOwner subdomain before standard parsing
	if owner, repo, ok := lookupRoute(r.Host); ok {
		log.Printf("Custom route matched: host=%s → owner=%s, repo=%s", r.Host, owner, repo)
		serveDeployedPage(w, r, owner, repo, "")
		return
	}

	if strings.ContainsAny(repoOwner, ".,/") {
		log.Printf("User hit URL with malformed owner subdomain (Host: %s, path: %s)", r.Host, r.URL)
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
		tempPage := NewForgePage(repoOwner, repoPath, urlHelper.GetElement(1))
		if tempPage.Exists() {
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
	page := NewForgePage(owner, repo, addBasePath)
	if !page.Exists() {
		log.Printf("Requested path %s empty or does not exist (Host: %s, path: %s)", page.StoragePath, r.Host, r.URL)
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "Not found: no page deployed for repo %s/%s", owner, repo)
		return
	}

	// check if oauth2 check is required
	if page.HasProtectionFlag() {
		log.Printf("Protected page hit (Host: %s, path: %s)", r.Host, r.URL)

		// get access token or redirect to login if not found
		tokenIface := sessionManager.Get(r.Context(), "access_token")
		token, ok := tokenIface.(oauth2.Token)
		if !ok {
			log.Println("access_token not found in session")

			// set redirect target, then do redirect to oauth
			fullURL := getFullURL(r)
			sessionManager.Put(r.Context(), "redirect_to", fullURL)
			log.Printf("redirect_url set to %s, redirecting to /login", fullURL)
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
	assetsPathRequest := page.StoragePath
	if addBasePath != "" {
		assetsPathRequest = filepath.Join(assetsPathRequest, filepath.Clean(urlHelper.GetElementsStartingFromElement(2)))
	} else if urlHelper.NumOfElements > 0 {
		// Standard path-based routing: first element is repo name, rest is asset path
		assetsPathRequest = filepath.Join(assetsPathRequest, filepath.Clean(urlHelper.GetElementsStartingFromElement(1)))
	}
	// if urlHelper.NumOfElements == 0 (custom route root path), assetsPathRequest stays as page.StoragePath

	log.Println("Serving asset " + assetsPathRequest)
	http.ServeFile(w, r, assetsPathRequest)
}

func getFullURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s%s", scheme, r.Host, r.URL.String())
}
