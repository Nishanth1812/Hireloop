package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"hireloop/api/db/migrations"
	"hireloop/api/internal/applications"
	"hireloop/api/internal/clerk"
	"hireloop/api/internal/httpapi"
	"hireloop/api/internal/identities"
	"hireloop/api/internal/reminders"
	"hireloop/api/internal/tasks"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	var store applications.Store
	var taskStore tasks.Store
	var reminderStore *reminders.Store
	var db *sql.DB
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		var err error
		db, err = prepareDatabase(databaseURL, migrations.Apply)
		if err != nil {
			log.Fatal("database initialization or migration failed; check database configuration and migration state")
		}
		defer db.Close()
		reminderStore = &reminders.Store{DB: db}
		store = applications.PostgresStore{DB: db, Jobs: reminderStore}
		taskStore = tasks.PostgresStore{DB: db, Jobs: reminderStore}
	}

	// Clerk authentication (migration target). Supabase remains the database.
	// Configure CLERK_JWKS_URL and CLERK_ISSUER to verify Clerk session tokens.
	// CLERK_ISSUER is the instance's Frontend API origin. Clerk session tokens
	// carry no audience by default, so there is no audience to configure (the
	// app ID is not the audience). CLERK_AUTHORIZED_PARTIES optionally lists
	// the origins (comma separated) permitted to mint tokens, matched against
	// the token's `azp` claim. Authentication fails closed when Clerk is unset.
	var clerkVerifier *clerk.Verifier
	issuer, jwksURL := os.Getenv("CLERK_ISSUER"), os.Getenv("CLERK_JWKS_URL")
	if (issuer == "") != (jwksURL == "") {
		log.Fatal("CLERK_ISSUER and CLERK_JWKS_URL must be configured together")
	}
	if jwksURL != "" {
		var err error
		clerkVerifier, err = clerk.NewVerifier(issuer, jwksURL)
		if err != nil {
			log.Fatalf("invalid Clerk configuration: %v", err)
		}
		if parties := os.Getenv("CLERK_AUTHORIZED_PARTIES"); parties != "" {
			for _, party := range strings.Split(parties, ",") {
				if party = strings.TrimSpace(party); party != "" {
					clerkVerifier.AuthorizedParties = append(clerkVerifier.AuthorizedParties, party)
				}
			}
		}
	}

	// Identity mapping: binds each verified Clerk subject to its existing
	// Supabase auth.users owner. Only built when a database is configured.
	var identityMapper identities.Mapper
	if db != nil {
		identityMapper = identities.PostgresMapper{DB: db}
	}

	address := os.Getenv("HIRELOOP_API_ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	server := &http.Server{
		Addr:              address,
		Handler:           httpapi.NewHandlerWithTasks(clerkVerifier, identityMapper, store, taskStore, reminderStore),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("Hireloop API listening on http://%s", server.Addr)
	log.Fatal(server.ListenAndServe())
}

func prepareDatabase(databaseURL string, apply func(*sql.DB) error) (*sql.DB, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := apply(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
