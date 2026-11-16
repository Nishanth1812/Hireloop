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

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	var store applications.Store
	var db *sql.DB
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		var err error
		db, err = prepareDatabase(databaseURL, migrations.Apply)
		if err != nil {
			log.Fatal("database initialization or migration failed; check database configuration and migration state")
		}
		defer db.Close()
		store = applications.PostgresStore{DB: db}
	}

	// Clerk authentication (migration target). Supabase remains the database.
	// Configure CLERK_JWKS_URL and CLERK_ISSUER to verify Clerk session tokens.
	// CLERK_ISSUER is the instance's Frontend API origin. Clerk session tokens
	// carry no audience by default, so there is no audience to configure (the
	// app ID is not the audience). CLERK_AUTHORIZED_PARTIES optionally lists
	// the origins (comma separated) permitted to mint tokens, matched against
	// the token's `azp` claim. With Clerk unset, the API falls back to
	// Supabase Auth verification, keeping the change reversible.
	var clerkVerifier *clerk.Verifier
	if jwksURL := os.Getenv("CLERK_JWKS_URL"); jwksURL != "" {
		issuer := os.Getenv("CLERK_ISSUER")
		if issuer == "" {
			log.Fatal("CLERK_ISSUER is required when CLERK_JWKS_URL is set")
		}
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

	server := &http.Server{
		Addr:              "127.0.0.1:8080",
		Handler:           httpapi.NewHandler(os.Getenv("SUPABASE_URL"), os.Getenv("SUPABASE_PUBLISHABLE_KEY"), nil, clerkVerifier, identityMapper, store),
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
