// Package storage owns SQLite migrations, catalog transactions and job evidence.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
	_ "modernc.org/sqlite"
)

const SchemaVersion = 1

var tables = map[string]string{"family": "app_families", "release": "app_releases", "instance": "app_instances", "environment": "environments", "binding": "file_bindings", "capability": "capability_results", "recipe": "recipes", "artifact": "artifacts", "plan": "plans", "job": "jobs"}

// Store is one process's database connection pool. External work must run outside transactions.
type Store struct{ DB *sql.DB }
type Event struct {
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}

// Open creates a private database and applies versioned migrations. Future versions fail closed.
func Open(path string) (*Store, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: database path must be absolute", domain.ErrInvalid)
	}
	if err := infra.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if st, err := os.Lstat(path); err == nil {
		if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: database is not a regular file", domain.ErrInvalid)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("_txlock", "immediate")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db}
	if err = s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > SchemaVersion {
		return fmt.Errorf("unsupported database schema %d", version)
	}
	if version == 0 {
		statements := []string{
			`CREATE TABLE records(kind TEXT NOT NULL,id TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,state TEXT NOT NULL DEFAULT '',parent_id TEXT NOT NULL DEFAULT '',backend TEXT NOT NULL DEFAULT '',PRIMARY KEY(kind,id))`,
			`CREATE INDEX records_state ON records(kind,state)`,
			`CREATE INDEX records_parent ON records(kind,parent_id)`,
			`CREATE TABLE resource_refs(from_kind TEXT NOT NULL,from_id TEXT NOT NULL,to_kind TEXT NOT NULL,to_id TEXT NOT NULL,role TEXT NOT NULL,PRIMARY KEY(from_kind,from_id,to_kind,to_id,role),FOREIGN KEY(from_kind,from_id) REFERENCES records(kind,id) ON DELETE CASCADE,FOREIGN KEY(to_kind,to_id) REFERENCES records(kind,id) ON DELETE RESTRICT)`,
			`CREATE INDEX refs_target ON resource_refs(to_kind,to_id)`,
			`CREATE TABLE idempotency(key TEXT PRIMARY KEY,plan_id TEXT NOT NULL,job_id TEXT NOT NULL UNIQUE)`,
			`CREATE TABLE events(seq INTEGER PRIMARY KEY AUTOINCREMENT,type TEXT NOT NULL,data TEXT NOT NULL CHECK(json_valid(data)),created_at TEXT NOT NULL)`,
		}
		for kind, table := range tables {
			statements = append(statements, fmt.Sprintf(`CREATE TABLE %s(kind TEXT NOT NULL DEFAULT '%s' CHECK(kind='%s'),id TEXT PRIMARY KEY,schema_version INTEGER NOT NULL DEFAULT 1 CHECK(schema_version=1),data TEXT NOT NULL CHECK(json_valid(data)),FOREIGN KEY(kind,id) REFERENCES records(kind,id) ON DELETE CASCADE)`, table, kind, kind))
		}
		statements = append(statements, `PRAGMA user_version=1`)
		for _, stmt := range statements {
			if _, err = tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
func validate(r domain.Record) error {
	if _, ok := tables[r.Kind]; !ok || r.ID == "" || len(r.ID) > 256 || strings.ContainsAny(r.ID, "/\\\x00") || !json.Valid(r.Data) || len(r.Data) > 4<<20 {
		return fmt.Errorf("%w: record kind, identity or data", domain.ErrInvalid)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(r.Data, &obj); err != nil || obj == nil {
		return fmt.Errorf("%w: record data must be object", domain.ErrInvalid)
	}
	return nil
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func put(ctx context.Context, q queryer, r domain.Record) error {
	if err := validate(r); err != nil {
		return err
	}
	if r.Kind == "plan" {
		var existing string
		err := q.QueryRowContext(ctx, "SELECT data FROM plans WHERE id=?", r.ID).Scan(&existing)
		if err == nil {
			if string(r.Data) == existing {
				return nil
			}
			return fmt.Errorf("%w: plans are immutable", domain.ErrConflict)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	var fields map[string]any
	json.Unmarshal(r.Data, &fields)
	value := func(k string) string { v, _ := fields[k].(string); return v }
	parent := value("family_id")
	for _, k := range []string{"release_id", "instance_id", "plan_id"} {
		if v := value(k); v != "" {
			parent = v
		}
	}
	created := r.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	updated := time.Now().UTC()
	if _, err := q.ExecContext(ctx, `INSERT INTO records(kind,id,created_at,updated_at,state,parent_id,backend) VALUES(?,?,?,?,?,?,?) ON CONFLICT(kind,id) DO UPDATE SET updated_at=excluded.updated_at,state=excluded.state,parent_id=excluded.parent_id,backend=excluded.backend`, r.Kind, r.ID, created.Format(time.RFC3339Nano), updated.Format(time.RFC3339Nano), value("state"), parent, value("backend")); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO `+tables[r.Kind]+`(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, r.ID, string(r.Data))
	if err != nil {
		return err
	}
	return recordChanged(ctx, q, r.Kind, r.ID, false)
}
func (s *Store) Put(ctx context.Context, r domain.Record) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = put(ctx, tx, r); err != nil {
		return err
	}
	return tx.Commit()
}
func get(ctx context.Context, q queryer, kind, id string) (domain.Record, error) {
	var r domain.Record
	table, ok := tables[kind]
	if !ok {
		return r, domain.ErrInvalid
	}
	var data, created, updated string
	err := q.QueryRowContext(ctx, `SELECT r.kind,r.id,t.data,r.created_at,r.updated_at FROM records r JOIN `+table+` t ON t.id=r.id WHERE r.kind=? AND r.id=?`, kind, id).Scan(&r.Kind, &r.ID, &data, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return r, domain.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Data = json.RawMessage(data)
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return r, nil
}
func (s *Store) Get(ctx context.Context, kind, id string) (domain.Record, error) {
	return get(ctx, s.DB, kind, id)
}
func (s *Store) List(ctx context.Context, kind string) ([]domain.Record, error) {
	table, ok := tables[kind]
	if !ok {
		return nil, domain.ErrInvalid
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT r.kind,r.id,t.data,r.created_at,r.updated_at FROM records r JOIN `+table+` t ON t.id=r.id WHERE r.kind=? AND NOT (r.kind IN ('instance','binding','capability') AND r.state='forgotten') ORDER BY r.created_at,r.id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Record{}
	for rows.Next() {
		var r domain.Record
		var data, created, updated string
		if err = rows.Scan(&r.Kind, &r.ID, &data, &created, &updated); err != nil {
			return nil, err
		}
		r.Data = json.RawMessage(data)
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) Delete(ctx context.Context, kind, id string) error {
	if _, ok := tables[kind]; !ok {
		return domain.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_refs WHERE to_kind=? AND to_id=?`, kind, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return domain.ErrReferenced
	}
	if kind == "plan" || kind == "job" {
		return fmt.Errorf("%w: execution evidence is retained", domain.ErrConflict)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM records WHERE kind=? AND id=?`, kind, id)
	if err != nil {
		return err
	}
	n64, _ := res.RowsAffected()
	if n64 == 0 {
		return domain.ErrNotFound
	}
	if err = recordChanged(ctx, tx, kind, id, true); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) References(ctx context.Context, kind, id string) ([]domain.ResourceRef, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT from_kind,from_id,to_kind,to_id,role FROM resource_refs WHERE to_kind=? AND to_id=? ORDER BY from_kind,from_id,role`, kind, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ResourceRef{}
	for rows.Next() {
		var r domain.ResourceRef
		if err = rows.Scan(&r.FromKind, &r.FromID, &r.ToKind, &r.ToID, &r.Role); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) Events(ctx context.Context, after int64, limit int) ([]Event, error) {
	if after < 0 || limit < 1 || limit > 1000 {
		return nil, domain.ErrInvalid
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT seq,type,data,created_at FROM events WHERE seq>? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var data, created string
		if err = rows.Scan(&e.Seq, &e.Type, &data, &created); err != nil {
			return nil, err
		}
		e.Data = json.RawMessage(data)
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateRecord atomically reads and changes one existing record. The callback
// must not perform external work or access Store (the transaction owns its pool
// connection). Identity and kind cannot change; plans remain immutable.
func (s *Store) UpdateRecord(ctx context.Context, kind, id string, update func(*domain.Record) error) error {
	if update == nil {
		return domain.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := get(ctx, tx, kind, id)
	if err != nil {
		return err
	}
	if err = update(&r); err != nil {
		return err
	}
	if r.Kind != kind || r.ID != id {
		return fmt.Errorf("%w: record identity cannot change", domain.ErrInvalid)
	}
	if err = put(ctx, tx, r); err != nil {
		return err
	}
	return tx.Commit()
}
