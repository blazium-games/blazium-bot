package main

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// One Postgres pool for the whole bot (POSTGRES_CON_STRING). Features that need it
// (marketing, sign-up panels) take it from botDatabase(); without it they switch off
// and the rest of the bot runs as usual.

var (
	botDBOnce sync.Once
	botDB     *sql.DB
)

// botDatabase opens the shared pool on first use. It returns nil when POSTGRES_CON_STRING
// is unset or the database can't be reached (the error is logged once).
func botDatabase() *sql.DB {
	botDBOnce.Do(func() {
		connString := strings.TrimSpace(os.Getenv("POSTGRES_CON_STRING"))
		if connString == "" {
			appLogger.Warn("POSTGRES_CON_STRING is not set; features that store data are disabled")
			return
		}
		db, err := openPostgres(connString)
		if err != nil {
			appLogger.Errorf("Postgres unavailable: %v", err)
			return
		}
		botDB = db
	})
	return botDB
}

func openPostgres(connString string) (*sql.DB, error) {
	db, err := sql.Open("pgx", connString)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func closeBotDatabase() {
	if botDB != nil {
		_ = botDB.Close()
	}
}

// ensureTables runs CREATE ... IF NOT EXISTS statements in order.
func ensureTables(ctx context.Context, db *sql.DB, stmts []string) error {
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
