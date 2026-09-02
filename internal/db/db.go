// Package db provides read-only access to the opencode SQLite database.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the underlying SQLite connection.
type DB struct {
	sql *sql.DB
}

// Open opens the opencode database at the given path (read-only).
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, fmt.Errorf("database path is empty")
	}
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(5000)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &DB{sql: sqlDB}, nil
}

// Close closes the underlying connection.
func (d *DB) Close() error {
	if d == nil || d.sql == nil {
		return nil
	}
	return d.sql.Close()
}

// SQL returns the underlying *sql.DB handle for advanced queries.
func (d *DB) SQL() *sql.DB {
	if d == nil {
		return nil
	}
	return d.sql
}

// DefaultPath returns the default opencode database path for the current user.
func DefaultPath() string {
	if p := os.Getenv("OPENCODE_DB"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "opencode.db"
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

// Session is a summary row of a session.
type Session struct {
	ID               string
	Title            string
	Directory        string
	Path             string
	Agent            string
	Model            string
	Cost             float64
	TokensInput      int
	TokensOutput     int
	TokensReasoning  int
	TokensCacheRead  int
	TokensCacheWrite int
	TimeCreated      time.Time
	TimeUpdated      time.Time
	MessageCount     int
	ToolCallCount    int
}

// ListSessions returns all sessions ordered by most recently updated.
func (d *DB) ListSessions() ([]Session, error) {
	rows, err := d.sql.Query(`
		SELECT
			s.id,
			s.title,
			s.directory,
			s.path,
			s.agent,
			s.model,
			s.cost,
			s.tokens_input,
			s.tokens_output,
			s.tokens_reasoning,
			s.tokens_cache_read,
			s.tokens_cache_write,
			s.time_created,
			s.time_updated,
			(SELECT COUNT(*) FROM message m WHERE m.session_id = s.id) AS msg_count,
			(SELECT COUNT(*) FROM part p WHERE p.session_id = s.id AND json_extract(p.data, '$.type') = 'tool') AS tool_count
		FROM session s
		ORDER BY s.time_updated DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var s Session
		var tc, tu int64
		var agent, model, dir, path sql.NullString
		if err := rows.Scan(
			&s.ID, &s.Title, &dir, &path, &agent, &model,
			&s.Cost, &s.TokensInput, &s.TokensOutput, &s.TokensReasoning,
			&s.TokensCacheRead, &s.TokensCacheWrite, &tc, &tu,
			&s.MessageCount, &s.ToolCallCount,
		); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		s.Agent = agent.String
		s.Model = model.String
		s.Directory = dir.String
		s.Path = path.String
		s.TimeCreated = time.UnixMilli(tc)
		s.TimeUpdated = time.UnixMilli(tu)
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}
