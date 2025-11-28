package main

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"
)

func handlePage(w http.ResponseWriter, r *http.Request) {
	// check host
	if r.Host == config.GetPagesURLHostOnly() { // request is exactly for http[s]://<page_url>/ --> fallback response
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello!"))
		return
	}
	if !strings.HasSuffix(r.Host, "."+config.GetPagesURLHostOnly()) { // request is <owner>.<page_url>
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: host does not end in configured pages_url (host part only)"))
		return
	}

	// get and check repo owner
	repoOwner := strings.TrimSuffix(r.Host, "."+config.GetPagesURLHostOnly())
	if strings.ContainsAny(repoOwner, ".,/") {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: repo owner (subdomain) is malformed"))
		return
	}

	// get path for repo
	if r.URL.Path == "/" || r.URL.Path == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: pages request sent to root folder"))
		return
	}
	pathParts := strings.SplitN(r.URL.Path, "/", 3)
	if len(pathParts) < 2 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: pages request path has not enough segments"))
		return
	}
	repoPath := pathParts[1] // [0] ist empty string

	// assemble page struct
	page := NewForgePage(repoOwner, repoPath)
	if !page.Exists() {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "Not found: no page deployed for repo %s/%s", repoOwner, repoPath)
		return
	}

	// check if oauth2 check is required
	if page.HasProtectionFlag() {
		// get access token or redirect to login if not found
		tokenIface := sessionManager.Get(r.Context(), "access_token")
		token, ok := tokenIface.(oauth2.Token)
		if !ok {
			// set redirect target, then do redirect to oauth
			fullURL := getFullURL(r)
			sessionManager.Put(r.Context(), "redirect_to", fullURL)
			http.Redirect(w, r, config.PagesURL+"/login", http.StatusFound)
			return
		}

		// generate client from access token and check permissions
		client := oauthConf.Client(r.Context(), &token)

		readable, err := ForgeCheckRepoReadableWithClient(fmt.Sprintf("%s/%s", repoOwner, repoPath), client)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Server error: unable to check permissions on the target repository"))
			return
		}
		if !readable {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("Unauthorized: no read access for you :("))
			return
		}
		// if we are here, OAuth2 was successfull and user has sufficient permissions. Continue serving assets.
	}

	// deliver static content
	assetsPathRequest := page.BasePath
	if len(pathParts) > 2 {
		assetsPathRequest = filepath.Join(assetsPathRequest, filepath.Clean(pathParts[2]))
	}

	log.Println("Serving asset: " + assetsPathRequest)
	http.ServeFile(w, r, assetsPathRequest)
}

func getFullURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s%s", scheme, r.Host, r.URL.String())
}
