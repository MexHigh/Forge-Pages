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
	repo, accessToken, addBasePath, protect, err := getParams(r.URL.Query())
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
	forgePage := NewForgePage(repoParts[0], repoParts[1], addBasePath)

	// init+clear directory
	log.Printf("Re-creating page path %s", forgePage.StoragePath)
	if err := os.RemoveAll(forgePage.StoragePath); err != nil {
		log.Printf("Error removing directory: %s", err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Server error: could not delete old page directory"))
		return
	}
	if err := os.MkdirAll(forgePage.StoragePath, 0750); err != nil {
		log.Printf("Error while creating directory: %s", err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Server error: could not create page directory"))
		return
	}

	// read and extract tar.gz file
	log.Println("Reading body")
	if err := extractTarGz(r.Body, forgePage.StoragePath); err != nil {
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
	repo, accessToken, addBasePath, _, err := getParams(r.URL.Query())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: " + err.Error()))
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
	forgePage := NewForgePage(repoParts[0], repoParts[1], addBasePath)
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

func getParams(urlQuery url.Values) (repo, accessToken, addBasePath string, protect bool, err error) {
	repo = urlQuery.Get("repo")
	if repo == "" {
		err = errors.New("missing parameter: repo")
		return
	}
	accessToken = urlQuery.Get("access_token")
	if accessToken == "" && !*skipDeployChecks {
		err = errors.New("missing parameter: access_token")
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
