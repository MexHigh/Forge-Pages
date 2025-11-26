package main

import (
	"fmt"
	"log"
	"net/http"
	"encoding/gob"
	"time"

	"golang.org/x/oauth2"
	"github.com/alexedwards/scs/v2"
	"github.com/google/uuid"
	"io"
)

var sessionManager *scs.SessionManager

var (
	oauthConf = &oauth2.Config{
		ClientID:     "TODO", // TODO config
		ClientSecret: "TODO", // TODO config
		RedirectURL:  "http://localhost:8080/callback", // TODO automatic
		Scopes:       []string{"openid", "profile"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://TODO/login/oauth/authorize", // TODO config
			TokenURL: "https://TODO/login/oauth/access_token", // TODO config
		},
	}
)

func init() {
	gob.Register(&oauth2.Token{})
}

func main() {
	sessionManager = scs.New()
	sessionManager.Lifetime = 1 * time.Hour

	mux := http.NewServeMux()

	mux.HandleFunc("/login", handleLogin)
	mux.HandleFunc("/callback", handleCallback)

	mux.HandleFunc("/page1", func(w http.ResponseWriter, r *http.Request) {
		// not secured
		fmt.Fprintf(w, "SUCCESS!")
	})

	mux.HandleFunc("/page2", func(w http.ResponseWriter, r *http.Request) {
		// secured

		// get access token or redirect to login if not found
		tokenIface := sessionManager.Get(r.Context(), "access_token")
		token, ok := tokenIface.(*oauth2.Token)
		if !ok {
			// set redirect target, then do redirect to oauth
			sessionManager.Put(r.Context(), "redirect_to", "http://localhost:8080/page2")
			http.Redirect(w, r, "http://localhost:8080/login", http.StatusFound)
			return
			// TODO redirect to login if nonexistent
		}

		// generate client from access token
		client := oauthConf.Client(r.Context(), token)

		// check, if user has access to this page
		// TODO
		resp, err := client.Get("https://TODO/api/v1/user")
		if err != nil {
			panic(err)
		}
		str, err := io.ReadAll(resp.Body)
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(w, string(str))
	})

	log.Println("Listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", sessionManager.LoadAndSave(mux)))
}

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
	sessionManager.Put(r.Context(), "access_token", token)

	// print some stuff
	/*fmt.Fprintf(w, "hello world\n\n")
	fmt.Fprintf(w, "Access Token: %s\n", token.AccessToken)
	if token.RefreshToken != "" {
		fmt.Fprintf(w, "Refresh Token: %s\n", token.RefreshToken)
	}
	fmt.Fprintf(w, "Token Type: %s\n", token.TokenType)*/

	// redirect and delete target
	redirURL := sessionManager.PopString(r.Context(), "redirect_to")
	if redirURL != "" {
		http.Redirect(w, r, redirURL, http.StatusFound)
	} else {
		// redir back to base url
		http.Redirect(w, r, "/", http.StatusFound)
	}
}
