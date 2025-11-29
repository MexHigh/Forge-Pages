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

func handleDeploy(w http.ResponseWriter, r *http.Request) {
	log.Printf("Incoming request to /deploy (Host: %s, path: %s)", r.Host, r.URL)

	switch r.Method {
	case http.MethodPost:
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
	repo, accessToken, protect, err := getRepoAndKey(r.URL.Query())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: " + err.Error()))
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
	forgePage := NewForgePage(repoParts[0], repoParts[1])

	// init+clear directory
	log.Printf("Re-creating page path %s", forgePage.BasePath)
	if err := os.RemoveAll(forgePage.BasePath); err != nil {
		log.Printf("Error removing directory: %s", err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Server error: could not delete old page directory"))
		return
	}
	if err := os.MkdirAll(forgePage.BasePath, 0750); err != nil {
		log.Printf("Error while creating directory: %s", err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Server error: could not create page directory"))
		return
	}

	// read and extract tar.gz file
	log.Println("Reading body")
	if err := extractTarGz(r.Body, forgePage.BasePath); err != nil {
		log.Printf("Error while reading .tar.gz body: %s; deleting deployment", err.Error())
		if err := forgePage.Purge(); err != nil {
			log.Printf("Error deleting deployment %s", err.Error())
		}
		// filter some errors to send accurate codes
		if strings.HasPrefix(err.Error(), "tar entry too large") || strings.HasPrefix(err.Error(), "upacked more than") {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		} else {
		w.WriteHeader(http.StatusInternalServerError)
		}
		w.Write([]byte("Server error: could not extract tar.gz"))
		return
	}

	// add protect flag
	if protect && !forgePage.HasProtectionFlag() { // this allows for adding the protect flag directly inside the tar.gz archive
		log.Println("User requested protection, adding flag")
		forgePage.AddProtectionFlag()
	}

	log.Printf("Success, deployed to %s.%s/%s", repoParts[0], config.GetPagesURLHostOnly(), repoParts[1])
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "Success, deployed to %s.%s/%s", repoParts[0], config.GetPagesURLHostOnly(), repoParts[1])
}

func handleDeleteDeployment(w http.ResponseWriter, r *http.Request) {
	// get params
	repo, accessToken, _, err := getRepoAndKey(r.URL.Query())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: " + err.Error()))
		return
	}
	log.Printf("Someone is trying to delete page for repo %s, checking access", repo)

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

	// check repo param syntax
	repoParts := strings.Split(repo, "/")
	if len(repoParts) != 2 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: repo does not have exactly one /"))
		return
	}

	// do the delete
	forgePage := NewForgePage(repoParts[0], repoParts[1])
	log.Printf("Deleting page path %s", forgePage.BasePath)
	if err := forgePage.Purge(); err != nil {
		log.Printf("Error deleting deployment %s", err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Server error: could not delete page directory"))
		return
	}

	log.Printf("Success, deleted %s.%s/%s", repoParts[0], config.GetPagesURLHostOnly(), repoParts[1])
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Success, deleted %s.%s/%s", repoParts[0], config.GetPagesURLHostOnly(), repoParts[1])
}

func getRepoAndKey(urlQuery url.Values) (string, string, bool, error) {
	repo := urlQuery.Get("repo")
	if repo == "" {
		return "", "", false, errors.New("missing parameter: repo")
	}
	accessToken := urlQuery.Get("access_token")
	if accessToken == "" && !*skipDeployChecks {
		return "", "", false, errors.New("missing parameter: access_token")
	}
	return repo, accessToken, urlQuery.Has("protect"), nil
}
