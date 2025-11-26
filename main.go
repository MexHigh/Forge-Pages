package main

import (
	"encoding/gob"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"
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

	if err := LoadConfig(*configPath); err != nil {
		panic(err) // config is required
	}

	// set OAuth config
	oauthConf = &oauth2.Config{
		ClientID:     config.OIDC.ID,
		ClientSecret: config.OIDC.Secret,
		RedirectURL:  config.PagesURL + "/callback",
		Scopes:       []string{"openid", "profile"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  config.OIDC.AuthURL,
			TokenURL: config.OIDC.TokenURL,
		},
	}

	// initialize session manager
	sessionManager = scs.New()
	sessionManager.Lifetime = 1 * time.Hour

	// router
	mux := http.NewServeMux()

	// oauth routes
	mux.HandleFunc("/login", handleLogin)
	mux.HandleFunc("/callback", handleCallback)

	mux.HandleFunc("/", handlePage)

	// pages routes
	/*mux.HandleFunc("/page1", func(w http.ResponseWriter, r *http.Request) {
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
			sessionManager.Put(r.Context(), "redirect_to", config.PagesURL+"/page2")
			http.Redirect(w, r, config.PagesURL+"/login", http.StatusFound)
			return
		}

		// generate client from access token
		client := oauthConf.Client(r.Context(), token)

		// check, if user has access to this page
		// TODO
		resp, err := client.Get(config.ForgeURL + "/api/v1/user")
		if err != nil {
			panic(err)
		}
		str, err := io.ReadAll(resp.Body)
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(w, "%s", string(str))
	})*/

	log.Println("Started server on port :8080")
	log.Fatal(http.ListenAndServe(":8080", sessionManager.LoadAndSave(mux)))
}
