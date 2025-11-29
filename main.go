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

// flags
var (
	configPath       = flag.String("config", "./config.yml", "Path to the YAML config file")
	skipDeployChecks = flag.Bool("skip_deploy_checks", false, "If set, the deploy route does not verify the repository or the access_token parameters and always deploys")
)

// public vars
var sessionManager *scs.SessionManager

// consts
const (
	pagesBind          = "0.0.0.0:8080"
	maxDeploySizeBytes = 20971520 // 20 MB
)

func init() {
	gob.Register(oauth2.Token{})
}

func main() {
	flag.Parse()

	log.Printf("Loading config from %s and running init steps", *configPath)
	if err := LoadConfig(*configPath); err != nil {
		panic(err) // config is required
	}

	// set OAuth config
	oauthConf = &oauth2.Config{
		ClientID:     config.OAuth.ID,
		ClientSecret: config.OAuth.Secret,
		RedirectURL:  config.PagesURL + "/callback",
		Scopes:       []string{"openid", "profile"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  config.OAuth.AuthURL,
			TokenURL: config.OAuth.TokenURL,
		},
	}

	// initialize session manager
	sessionManager = scs.New()
	sessionManager.Lifetime = 1 * time.Hour
	sessionManager.Cookie.Domain = "." + config.GetPagesURLHostOnlyWithoutPort()

	// router
	mux := http.NewServeMux()

	// oauth routes
	mux.Handle("/login", withAuth(handleLogin))
	mux.Handle("/callback", withAuth(handleCallback))
	// api routes
	mux.Handle("/deploy", http.MaxBytesHandler( // does not need a session
		http.HandlerFunc(handleDeploy),
		maxDeploySizeBytes,
	))
	// pages route
	mux.Handle("/", withAuth(handlePage))

	log.Printf("Pages server started on %s, waiting for requests", pagesBind)
	log.Fatal(http.ListenAndServe(pagesBind, mux))
}

func withAuth(f func(w http.ResponseWriter, r *http.Request)) http.Handler {
	return sessionManager.LoadAndSave(http.HandlerFunc(f))
}
