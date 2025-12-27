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
		log.Println("User hit base URL (/)")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello!"))
		return
	}
	if !strings.HasSuffix(r.Host, "."+config.GetPagesURLHostOnly()) { // request is <owner>.<page_url>
		log.Printf("User hit URL not ending in pages_url (Host: %s, path: %s)", r.Host, r.URL)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: host does not end in configured pages_url (host part only)"))
		return
	}

	// get and check repo owner
	repoOwner := strings.TrimSuffix(r.Host, "."+config.GetPagesURLHostOnly())
	if strings.ContainsAny(repoOwner, ".,/") {
		log.Printf("User hit URL with malformed owner subdomain (Host: %s, path: %s)", r.Host, r.URL)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: repo owner (subdomain) is malformed"))
		return
	}

	// get path for repo
	urlHelper := NewURLPathHelper(r.URL.Path)
	if urlHelper.NumOfElements == 0 {
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

	// assemble page struct
	page := NewForgePage(repoOwner, repoPath, addBasePath)
	if !page.Exists() {
		log.Printf("Requested path %s empty or does not exist (Host: %s, path: %s)", page.StoragePath, r.Host, r.URL)
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "Not found: no page deployed for repo %s/%s", repoOwner, repoPath)
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
		readable, err := ForgeCheckRepoReadableWithClient(fmt.Sprintf("%s/%s", repoOwner, repoPath), client) // we should not use page.[Owner|Repo] here, since those are always lowercase
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
	assetsPathRequest := page.StoragePath
	if addBasePath != "" { // "if we have an existing addBasePath"
		assetsPathRequest = filepath.Join(assetsPathRequest, filepath.Clean(urlHelper.GetElementsStartingFromElement(2)))
	} else {
		assetsPathRequest = filepath.Join(assetsPathRequest, filepath.Clean(urlHelper.GetElementsStartingFromElement(1)))
	}

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
