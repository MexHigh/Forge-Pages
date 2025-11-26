package main

import (
	"encoding/gob"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"io"

	"github.com/alexedwards/scs/v2"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

var configPath = flag.String("config", "./config.yaml", "Path to the YAML config file")

var sessionManager *scs.SessionManager

func init() {
	gob.Register(&oauth2.Token{})
}

func main() {
	flag.Parse()

	// load config
	c, err := LoadConfig(*configPath)
	if err != nil {
		panic(err) // config is required
	}

	// set OAuth config
	oauthConf = &oauth2.Config{
		ClientID:     c.OIDC.ID,
		ClientSecret: c.OIDC.Secret,
		RedirectURL:  c.PagesURL + "/callback",
		Scopes:       []string{"openid", "profile"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  c.OIDC.AuthURL,
			TokenURL: c.OIDC.TokenURL,
		},
	}

	// initialize session manager
	sessionManager = scs.New()
	sessionManager.Lifetime = 1 * time.Hour

	// add routes
	mux := http.NewServeMux()

	// oauth routes
	mux.HandleFunc("/login", handleLogin)
	mux.HandleFunc("/callback", handleCallback)

	// pages routes
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
			sessionManager.Put(r.Context(), "redirect_to", c.PagesURL+"/page2")
			http.Redirect(w, r, c.PagesURL+"/login", http.StatusFound)
			return
			// TODO redirect to login if nonexistent
		}

		// generate client from access token
		client := oauthConf.Client(r.Context(), token)

		// check, if user has access to this page
		// TODO
		resp, err := client.Get(c.ForgeURL + "/api/v1/user")
		if err != nil {
			panic(err)
		}
		str, err := io.ReadAll(resp.Body)
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(w, "%s", string(str))
	})

	log.Println("Started server on port :8080")
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

	// redirect and delete target
	redirURL := sessionManager.PopString(r.Context(), "redirect_to")
	if redirURL != "" {
		http.Redirect(w, r, redirURL, http.StatusFound)
	} else {
		// redir back to base url
		http.Redirect(w, r, "/", http.StatusFound)
	}
}
