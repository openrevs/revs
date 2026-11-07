package dmrs

import (
	"database/sql"
	"log"
	"time"
)

// sessionStore implements scs.Store on the app database, replacing
// alexedwards/scs/sqlite3store so that module (and the mattn/go-sqlite3
// requirement it carries) drops out of the dependency graph. It is
// compatible with that store's schema and semantics — same sessions
// table (migration 002), same julianday expiry encoding — so sessions
// written by either implementation are read by both.
type sessionStore struct {
	db *sql.DB
}

// newSessionStore returns a store whose expired rows are cleaned up
// every few minutes for the life of the process.
func newSessionStore(db *sql.DB) *sessionStore {
	s := &sessionStore{db: db}
	go s.cleanupLoop()
	return s
}

// Find returns the data for a session token, with exists false for
// unknown or expired tokens.
func (s *sessionStore) Find(token string) ([]byte, bool, error) {
	var data []byte
	err := s.db.QueryRow(
		`SELECT data FROM sessions WHERE token = ? AND julianday('now') < expiry`,
		token).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	return data, true, nil
}

// Commit adds or replaces a session token with the given data and
// expiry time.
func (s *sessionStore) Commit(token string, data []byte, expiry time.Time) error {
	_, err := s.db.Exec(
		`REPLACE INTO sessions (token, data, expiry) VALUES (?, ?, julianday(?))`,
		token, data, expiry.UTC().Format("2006-01-02T15:04:05.999"))
	return err
}

// Delete removes a session token.
func (s *sessionStore) Delete(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func (s *sessionStore) cleanupLoop() {
	for range time.Tick(5 * time.Minute) {
		if _, err := s.db.Exec(`DELETE FROM sessions WHERE expiry < julianday('now')`); err != nil {
			log.Printf("session cleanup: %v", err)
		}
	}
}
