// Package sqlite is a core.Store kept in a SQLite file. The file belongs to the
// project: the project decides where it lives, who can read it and when it is
// deleted. This package only connects to it, creating the folder, the file and
// the tables when they are missing, and upgrading the tables when this
// program's schema is newer than the file's.
//
// The driver is modernc.org/sqlite, which is pure Go, so the program stays a
// single binary with no C compiler needed. It is imported only here.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	modsqlite "modernc.org/sqlite" // the driver; importing it registers "sqlite"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// busyTimeout is how long a program waits for another one that holds the file
// before giving up.
const busyTimeout = 5 * time.Second

// applicationID marks a SQLite file as a history file of this program
// ("CTM1"). It is stored in the file header, so an unrelated SQLite database
// is never mistaken for one.
const applicationID = 0x43544D31

// Store is a core.Store backed by a SQLite file.
type Store struct {
	db *sql.DB
}

var _ core.Store = (*Store)(nil)

// Open connects to the history file at path, creating the folder and the file
// (readable only by the owner) when they do not exist, then makes sure the
// tables are current.
//
// A file that is another SQLite database, or one written by a newer version of
// this program, is refused with an error and left unchanged.
func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, fmt.Errorf("store: creating the folder for the history file: %w", err)
	}
	// Create the file ourselves so it is private to the owner: saved output
	// could still hold something sensitive that scrubbing missed. An existing
	// file keeps its permissions.
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}

	// Per-connection settings that change nothing in the file. Write-ahead
	// logging is switched on later, once the file is known to be ours.
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	q.Add("_pragma", "foreign_keys(1)")
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	s := &Store{db: db}
	if err := s.prepare(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the connection.
func (s *Store) Close() error { return s.db.Close() }

// prepare checks that the file is ours (or new), upgrades the tables, and
// turns on write-ahead logging.
func (s *Store) prepare(ctx context.Context) error {
	var appID, version, tables int
	for pragma, dest := range map[string]*int{
		"PRAGMA application_id":              &appID,
		"PRAGMA user_version":                &version,
		"SELECT count(*) FROM sqlite_master": &tables,
	} {
		if err := s.db.QueryRowContext(ctx, pragma).Scan(dest); err != nil {
			return fmt.Errorf("store: reading the history file: %w", err)
		}
	}

	switch {
	case appID == applicationID:
		if version > len(migrations) {
			return fmt.Errorf("store: the history file was written by a newer version of this program (schema %d; this one reads up to %d); upgrade the program", version, len(migrations))
		}
	case appID == 0 && version == 0 && tables == 0:
		// A new, empty file.
	default:
		return errors.New("store: this file is not a history file of this program (it is another SQLite database); it was left unchanged")
	}

	if err := s.migrate(ctx); err != nil {
		return err
	}
	return s.enableWAL(ctx)
}

// enableWAL switches the file to write-ahead logging, which lets a push hook, a
// pull and the menu use it at once. The setting is stored in the file, so later
// connections inherit it and the switch happens only once.
//
// SQLite refuses the switch with "busy" immediately, without waiting for the
// busy timeout, when another connection is using the file. That happens when
// two programs open a brand-new file together. So the switch is skipped when
// another program has already made it, and otherwise retried until the timeout.
func (s *Store) enableWAL(ctx context.Context) error {
	deadline := time.Now().Add(busyTimeout)
	for {
		var mode string
		if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			return fmt.Errorf("store: %w", err)
		}
		if mode == "wal" {
			return nil
		}
		err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode)
		if err == nil {
			return nil
		}
		if !isBusy(err) || time.Now().After(deadline) {
			return fmt.Errorf("store: switching to write-ahead logging: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// isBusy reports whether err is SQLite saying the file is in use by another
// connection (SQLITE_BUSY, code 5).
func isBusy(err error) bool {
	var se *modsqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == 5
}

// migrate brings the tables to the current schema inside one transaction, so a
// failed upgrade leaves the file as it was. The version is read again inside
// the transaction, so two programs opening a new file together do not both
// create the tables.
func (s *Store) migrate(ctx context.Context) error {
	return s.writeTx(ctx, func(conn *sql.Conn) error {
		var version int
		if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		if version >= len(migrations) {
			return nil // already current: another program got here first
		}
		for v := version; v < len(migrations); v++ {
			for _, stmt := range migrations[v] {
				if _, err := conn.ExecContext(ctx, stmt); err != nil {
					return fmt.Errorf("store: upgrading the history file to schema %d: %w", v+1, err)
				}
			}
		}
		// PRAGMA cannot take parameters; both values are integers we control.
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", applicationID)); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(migrations)))
		return err
	})
}

// writeTx runs fn in a transaction that takes the write lock at once (BEGIN
// IMMEDIATE), so two writers queue behind the busy timeout instead of one
// failing halfway through. fn's error rolls the transaction back.
func (s *Store) writeTx(ctx context.Context, fn func(conn *sql.Conn) error) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			// The transaction is still open: close it, even when ctx is
			// already cancelled, so the connection goes back clean.
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	if err = fn(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}
