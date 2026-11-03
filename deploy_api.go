package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
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
	repo, accessToken, addBasePath, protect, err := getParams(r)
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

	// deploy with staging and rollback on failure
	log.Printf("Deploying to page path %s", forgePage.StoragePath)
	if err := forgePage.Deploy(r.Body, protect); err != nil {
		log.Printf("Error while deploying: %s", err.Error())
		// filter some errors to send accurate codes
		if strings.HasPrefix(err.Error(), "tar entry too large") || strings.HasPrefix(err.Error(), "upacked more than") {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
		w.Write([]byte("Server error: could not deploy page"))
		return
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
	repo, accessToken, addBasePath, _, err := getParams(r)
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

func getParams(r *http.Request) (repo, accessToken, addBasePath string, protect bool, err error) {
	urlQuery := r.URL.Query()
	repo = urlQuery.Get("repo")
	if repo == "" {
		err = errors.New("missing parameter: repo")
		return
	}
	accessToken = extractAccessToken(r, urlQuery)
	if accessToken == "" && !*skipDeployChecks {
		err = errors.New("missing parameter: access_token")
		return
	}
	addBasePath = urlQuery.Get("additional_base_path") // if not set --> empty string
	protect = urlQuery.Has("protect")
	return
}

// extractAccessToken prefers a Bearer token from the Authorization header and
// falls back to the access_token query parameter for backwards compatibility.
func extractAccessToken(r *http.Request, urlQuery url.Values) string {
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(authHeader, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
		}
	}
	return urlQuery.Get("access_token")
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
