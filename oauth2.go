package main

import (
	"net/http"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

var oauthConf *oauth2.Config

func handleLogin(w http.ResponseWriter, r *http.Request) {
	// generate new state and store in session manager
	newState := uuid.New().String()
	sessionManager.Put(r.Context(), "state", newState)

	// redirect to provider
	url := oauthConf.AuthCodeURL(newState, oauth2.AccessTypeOffline)
	http.Redirect(w, r, url, http.StatusFound)
}

func handleCallback(w http.ResponseWriter, r *http.Request) {
	// get and remove state from session
	sessState := sessionManager.PopString(r.Context(), "state")

	// check provided state with session state
	reqState := r.URL.Query().Get("state")
	if reqState != sessState {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	// get code and exchange for access token
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "no code in request", http.StatusBadRequest)
		return
	}
	token, err := oauthConf.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "token exchange failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// store access token in session
	sessionManager.Put(r.Context(), "access_token", *token)

	// redirect and delete target
	redirURL := sessionManager.PopString(r.Context(), "redirect_to")
	if redirURL != "" {
		http.Redirect(w, r, redirURL, http.StatusFound)
	} else {
		// redir back to base url
		http.Redirect(w, r, "/", http.StatusFound)
	}
}
