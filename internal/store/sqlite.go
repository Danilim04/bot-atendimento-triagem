package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // driver "sqlite" (puro Go, sem CGO)
)

// SQLiteStore implementa Store sobre SQLite via modernc.org/sqlite.
type SQLiteStore struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS conversation_state (
    conversation_id INTEGER PRIMARY KEY,
    account_id      INTEGER NOT NULL,
    state           TEXT    NOT NULL,
    data            TEXT    NOT NULL DEFAULT '',
    updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS csat_deadlines (
    conversation_id INTEGER PRIMARY KEY,
    account_id      INTEGER NOT NULL,
    deadline_at     INTEGER NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'pending'
);
CREATE INDEX IF NOT EXISTS idx_csat_deadlines_due
    ON csat_deadlines(status, deadline_at);

CREATE TABLE IF NOT EXISTS processed_events (
    delivery_id TEXT PRIMARY KEY,
    created_at  INTEGER NOT NULL
);
`

// NewSQLite abre (criando se necessário) o banco e aplica o schema.
func NewSQLite(path string) (*SQLiteStore, error) {
	dsn := path
	if !strings.Contains(dsn, "?") {
		dsn += "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("abrir sqlite: %w", err)
	}
	// SQLite tolera melhor um único escritor; o volume do bot é baixo.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrar schema: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) GetState(ctx context.Context, convID int64) (*ConversationState, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT conversation_id, account_id, state, data, updated_at
		   FROM conversation_state WHERE conversation_id = ?`, convID)

	var st ConversationState
	var updated int64
	err := row.Scan(&st.ConversationID, &st.AccountID, &st.State, &st.Data, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st.UpdatedAt = time.Unix(updated, 0)
	return &st, nil
}

func (s *SQLiteStore) SetState(ctx context.Context, st *ConversationState) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO conversation_state (conversation_id, account_id, state, data, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(conversation_id) DO UPDATE SET
		     account_id = excluded.account_id,
		     state      = excluded.state,
		     data       = excluded.data,
		     updated_at = excluded.updated_at`,
		st.ConversationID, st.AccountID, string(st.State), st.Data, time.Now().Unix())
	return err
}

func (s *SQLiteStore) MarkProcessed(ctx context.Context, deliveryID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO processed_events (delivery_id, created_at) VALUES (?, ?)
		 ON CONFLICT(delivery_id) DO NOTHING`,
		deliveryID, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *SQLiteStore) CreateDeadline(ctx context.Context, convID, accountID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO csat_deadlines (conversation_id, account_id, deadline_at, status)
		 VALUES (?, ?, ?, 'pending')
		 ON CONFLICT(conversation_id) DO UPDATE SET
		     account_id  = excluded.account_id,
		     deadline_at = excluded.deadline_at,
		     status      = 'pending'`,
		convID, accountID, at.Unix())
	return err
}

func (s *SQLiteStore) MarkDeadlineAnswered(ctx context.Context, convID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE csat_deadlines SET status='answered' WHERE conversation_id = ?`, convID)
	return err
}

func (s *SQLiteStore) ClaimExpiredDeadlines(ctx context.Context, now time.Time, limit int) ([]Deadline, error) {
	// Reivindica atomicamente prazos vencidos. Inclui status 'closing' para
	// reprocessar prazos de execuções anteriores interrompidas (idempotente,
	// pois resolver a conversa é idempotente).
	rows, err := s.db.QueryContext(ctx,
		`UPDATE csat_deadlines
		    SET status='closing'
		  WHERE status IN ('pending','closing') AND deadline_at <= ?
		 RETURNING conversation_id, account_id, deadline_at, status`,
		now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Deadline
	for rows.Next() {
		if limit > 0 && len(out) >= limit {
			break
		}
		var d Deadline
		var dl int64
		if err := rows.Scan(&d.ConversationID, &d.AccountID, &dl, &d.Status); err != nil {
			return nil, err
		}
		d.DeadlineAt = time.Unix(dl, 0)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) CompleteDeadline(ctx context.Context, convID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE csat_deadlines SET status='done' WHERE conversation_id = ?`, convID)
	return err
}

func (s *SQLiteStore) Close() error { return s.db.Close() }
