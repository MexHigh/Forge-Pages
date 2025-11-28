package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func handleDeploy(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		handleNewDeployment(w, r)
	case http.MethodDelete:
		handleDeleteDeployment(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("Method not allowed"))
	}
}

func handleNewDeployment(w http.ResponseWriter, r *http.Request) {
	// check stuff
	repo, accessToken, protect, err := checkRepoAndKey(r.URL.Query())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: " + err.Error()))
		return
	}
	if err := checkWriteAccessToRepo(repo, accessToken); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Unauthorized: user does not have write permissions on this repository"))
		return
	}
	repoParts := strings.Split(repo, "/")
	if len(repoParts) != 2 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: repo does not have exactly one /"))
		return
	}

	forgePage := NewForgePage(repoParts[0], repoParts[1])

	// init+clear directory
	if err := os.RemoveAll(forgePage.BasePath); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Server error: could not delete old page directory"))
		return
	}
	if err := os.MkdirAll(forgePage.BasePath, 0750); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Server error: could not create page directory"))
		return
	}

	// TODO check file size of tar.gz

	// read and extract tar.gz file
	if err := extractTarGz(r.Body, forgePage.BasePath); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Server error: could not extract tar.gz. Detailed error: " + err.Error()))
		return
	}

	// add protect flag
	if protect && !forgePage.HasProtectionFlag() { // this allows for adding the protect flag directly inside the tar.gz archive
		forgePage.AddProtectionFlag()
	}

	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "Success, deployed to %s.%s/%s", repoParts[0], config.GetPagesURLHostOnly(), repoParts[1])
}

func handleDeleteDeployment(w http.ResponseWriter, r *http.Request) {
	// check stuff
	repo, accessToken, _, err := checkRepoAndKey(r.URL.Query())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: " + err.Error()))
		return
	}
	if err := checkWriteAccessToRepo(repo, accessToken); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Unauthorized: user does not have write permissions on this repository"))
		return
	}
	repoParts := strings.Split(repo, "/")
	if len(repoParts) != 2 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bad request: repo does not have exactly one /"))
		return
	}

	// do the delete
	forgePage := NewForgePage(repoParts[0], repoParts[1])
	if err := os.RemoveAll(forgePage.BasePath); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Server error: could not delete page directory"))
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Success, deleted %s.%s/%s", repoParts[0], config.GetPagesURLHostOnly(), repoParts[1])
}

func checkRepoAndKey(urlQuery url.Values) (string, string, bool, error) {
	repo := urlQuery.Get("repo")
	if repo == "" {
		return "", "", false, errors.New("missing parameter: repo")
	}
	accessToken := urlQuery.Get("access_token")
	if accessToken == "" {
		return "", "", false, errors.New("missing parameter: access_token")
	}
	return repo, accessToken, urlQuery.Has("protect"), nil
}

func checkWriteAccessToRepo(repo, accessToken string) error {
	return nil // TODO
}

func extractTarGz(r io.Reader, dest string) error {
	// gzip reader
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()

	// tar reader
	tr := tar.NewReader(gz)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, filepath.Clean(header.Name)) // TODO check how to prevent path traversals outside the pages dir here

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}

	return nil
}
