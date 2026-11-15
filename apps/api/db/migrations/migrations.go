package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed *.up.sql
var files embed.FS

func NewSource() (source.Driver, error) { return iofs.New(files, ".") }

// Apply uses a dedicated session so the migration lock works with session pooling.
func Apply(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	driver, err := postgres.WithConnection(ctx, conn, &postgres.Config{StatementTimeout: 30 * time.Second})
	if err != nil {
		conn.Close()
		return err
	}
	src, err := NewSource()
	if err != nil {
		driver.Close()
		return err
	}
	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		src.Close()
		driver.Close()
		return err
	}
	defer m.Close()
	err = m.Up()
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}
