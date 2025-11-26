package main

import (
	"log"
	"net/http"
	"path/filepath"
	"strings"
)

func handlePage(w http.ResponseWriter, r *http.Request) {
	// get host for owner
	hostParts := strings.SplitN(r.Host, ".", 2)

	if len(hostParts) < 2 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: host does not contain repository owner"))
		return
	}

	if hostParts[1] != config.GetPagesURLHostOnly() {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: host does not end in configured pages_url"))
		return
	}
	repoOwner := hostParts[0]

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

	// check if oauth2 check is required
	if page.HasProtectionFlag() {
		// TODO Implement oauth
	}

	// deliver static content
	assetsPathRequest := page.BasePath
	if len(pathParts) > 2 {
		assetsPathRequest = filepath.Join(assetsPathRequest, filepath.Clean(pathParts[2]))
	}

	log.Println("Serving asset: " + assetsPathRequest)
	http.ServeFile(w, r, assetsPathRequest)
}
