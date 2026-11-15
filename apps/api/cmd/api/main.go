package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"
	"time"

	"hireloop/api/db/migrations"
	"hireloop/api/internal/applications"
	"hireloop/api/internal/httpapi"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	var store applications.Store
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		db, err := prepareDatabase(databaseURL, migrations.Apply)
		if err != nil {
			log.Fatal("database initialization or migration failed; check database configuration and migration state")
		}
		defer db.Close()
		store = applications.PostgresStore{DB: db}
	}
	server := &http.Server{
		Addr:              "127.0.0.1:8080",
		Handler:           httpapi.NewHandler(os.Getenv("SUPABASE_URL"), os.Getenv("SUPABASE_PUBLISHABLE_KEY"), nil, store),
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
