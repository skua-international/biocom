package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Alert represents a single watchdog alert row.
type Alert struct {
	ID        int64
	Level     string // "red", "yellow", "green"
	Container string
	Title     string
	Body      string
	RolePing  bool
	CreatedAt time.Time
	SentAt    *time.Time
}

// Store wraps SQLite operations for the alerts table.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and runs migrations.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// InsertAlert inserts a new unsent alert and returns its row ID.
func (s *Store) InsertAlert(ctx context.Context, a *Alert) (int64, error) {
	rolePing := 0
	if a.RolePing {
		rolePing = 1
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO alerts (level, container, title, body, role_ping, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		a.Level, a.Container, a.Title, a.Body, rolePing,
		a.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("insert alert: %w", err)
	}

	return result.LastInsertId()
}

// UnsentAlerts returns all alerts where sent_at IS NULL, ordered by id.
func (s *Store) UnsentAlerts(ctx context.Context) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, level, container, title, body, role_ping, created_at
		 FROM alerts
		 WHERE sent_at IS NULL
		 ORDER BY id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query unsent alerts: %w", err)
	}
	defer rows.Close()

	var alerts []Alert
	for rows.Next() {
		var a Alert
		var rolePing int
		var createdStr string

		if err := rows.Scan(&a.ID, &a.Level, &a.Container, &a.Title, &a.Body, &rolePing, &createdStr); err != nil {
			return nil, fmt.Errorf("scan alert row: %w", err)
		}

		a.RolePing = rolePing != 0
		a.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		alerts = append(alerts, a)
	}

	return alerts, rows.Err()
}

// MarkSent sets sent_at to the current time for the given alert ID.
func (s *Store) MarkSent(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE alerts SET sent_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		return fmt.Errorf("mark alert sent: %w", err)
	}
	return nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS alerts (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			level      TEXT    NOT NULL,
			container  TEXT    NOT NULL,
			title      TEXT    NOT NULL,
			body       TEXT    NOT NULL DEFAULT '',
			role_ping  INTEGER NOT NULL DEFAULT 0,
			created_at TEXT    NOT NULL,
			sent_at    TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_alerts_unsent ON alerts(sent_at) WHERE sent_at IS NULL;
	`)
	if err != nil {
		return fmt.Errorf("create alerts table: %w", err)
	}
	return nil
}
