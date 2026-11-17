package state

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Record is one file's latest decision.
type Record struct {
	Path        string
	Size        int64
	ModUnix     int64
	Decision    string
	Reason      string
	SavingsPct  float64
	SourceBytes int64
	OutputBytes int64
	CheckedUnix int64
	GPU         string
}

// Stats summarizes the database.
type Stats struct {
	Encoded    int
	Kept       int
	Skipped    int
	Failed     int
	SavedBytes int64
}

// Store is a SQLite database.
type Store struct {
	db *sql.DB
}

// Open creates the database and schema.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxLifetime(0)
	s := &Store{db: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	_, err := s.db.Exec(`
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS files (
  path TEXT PRIMARY KEY,
  size INTEGER NOT NULL,
  mod_unix INTEGER NOT NULL,
  decision TEXT NOT NULL,
  reason TEXT NOT NULL,
  savings_pct REAL NOT NULL,
  source_bytes INTEGER NOT NULL,
  output_bytes INTEGER NOT NULL,
  checked_unix INTEGER NOT NULL,
  gpu TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS files_checked ON files(checked_unix);
CREATE INDEX IF NOT EXISTS files_decision_size ON files(decision, size);
CREATE TABLE IF NOT EXISTS prescan (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  finished_unix INTEGER NOT NULL,
  files INTEGER NOT NULL,
  candidates INTEGER NOT NULL,
  skipped INTEGER NOT NULL,
  settling INTEGER NOT NULL,
  failed INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS prescan_reasons (
  reason TEXT PRIMARY KEY,
  n INTEGER NOT NULL
);`)
	return err
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Get returns a record.
func (s *Store) Get(path string) (Record, bool, error) {
	var r Record
	err := s.db.QueryRow(`SELECT path, size, mod_unix, decision, reason, savings_pct, source_bytes, output_bytes, checked_unix, gpu FROM files WHERE path = ?`, path).
		Scan(&r.Path, &r.Size, &r.ModUnix, &r.Decision, &r.Reason, &r.SavingsPct, &r.SourceBytes, &r.OutputBytes, &r.CheckedUnix, &r.GPU)
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	return r, true, nil
}

// Put inserts or replaces a record.
func (s *Store) Put(r Record) error {
	if r.CheckedUnix == 0 {
		r.CheckedUnix = time.Now().Unix()
	}
	_, err := s.db.Exec(`
INSERT INTO files (path, size, mod_unix, decision, reason, savings_pct, source_bytes, output_bytes, checked_unix, gpu)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET
  size=excluded.size,
  mod_unix=excluded.mod_unix,
  decision=excluded.decision,
  reason=excluded.reason,
  savings_pct=excluded.savings_pct,
  source_bytes=excluded.source_bytes,
  output_bytes=excluded.output_bytes,
  checked_unix=excluded.checked_unix,
  gpu=excluded.gpu`,
		r.Path, r.Size, r.ModUnix, r.Decision, r.Reason, r.SavingsPct, r.SourceBytes, r.OutputBytes, r.CheckedUnix, r.GPU)
	return err
}

// ShouldSkip reports whether a daily pass can ignore this unchanged file.
func (s *Store) ShouldSkip(path string, size int64, mod time.Time, retry time.Duration) (bool, string, error) {
	r, ok, err := s.Get(path)
	if err != nil || !ok {
		return false, "", err
	}
	if r.Size != size || r.ModUnix != mod.Unix() {
		return false, "", nil
	}
	switch r.Decision {
	case "encoded", "skipped":
		return true, r.Reason, nil
	case "queued":
		// A prescan marked this file as work. The encode pass still runs it.
		return false, "", nil
	case "kept":
		if time.Since(time.Unix(r.CheckedUnix, 0)) < retry {
			return true, r.Reason, nil
		}
	}
	return false, "", nil
}

// Summary aggregates decisions.
func (s *Store) Summary() (Stats, error) {
	rows, err := s.db.Query(`SELECT decision, COUNT(*), COALESCE(SUM(CASE WHEN decision='encoded' THEN source_bytes-output_bytes ELSE 0 END), 0) FROM files GROUP BY decision`)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	var st Stats
	for rows.Next() {
		var decision string
		var n int
		var saved int64
		if err := rows.Scan(&decision, &n, &saved); err != nil {
			return Stats{}, err
		}
		switch decision {
		case "encoded":
			st.Encoded = n
			st.SavedBytes += saved
		case "kept":
			st.Kept = n
		case "skipped":
			st.Skipped = n
		case "failed":
			st.Failed = n
		}
	}
	return st, rows.Err()
}

const recordCols = `path, size, mod_unix, decision, reason, savings_pct, source_bytes, output_bytes, checked_unix, gpu`

func scanRecords(rows *sql.Rows) ([]Record, error) {
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.Path, &r.Size, &r.ModUnix, &r.Decision, &r.Reason, &r.SavingsPct, &r.SourceBytes, &r.OutputBytes, &r.CheckedUnix, &r.GPU); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Recent returns the latest decision per file, newest first.
// before is an exclusive checked_unix bound; zero means now. An empty
// decisions list matches every decision.
func (s *Store) Recent(limit int, before int64, decisions []string) ([]Record, error) {
	if limit < 1 {
		limit = 50
	}
	q := `SELECT ` + recordCols + ` FROM files WHERE 1=1`
	var args []any
	if before > 0 {
		q += ` AND checked_unix < ?`
		args = append(args, before)
	}
	if len(decisions) > 0 {
		q += ` AND decision IN (?` + strings.Repeat(`, ?`, len(decisions)-1) + `)`
		for _, d := range decisions {
			args = append(args, d)
		}
	}
	q += ` ORDER BY checked_unix DESC, path LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

// Queued returns files waiting for a pass, largest first.
func (s *Store) Queued(limit int) ([]Record, error) {
	if limit < 1 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+recordCols+` FROM files WHERE decision = 'queued' ORDER BY size DESC, path LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

// ReasonCount is one prescan skip bucket.
type ReasonCount struct {
	Reason string
	N      int
}

// Prescan is the last completed library count.
type Prescan struct {
	Finished   time.Time
	Files      int
	Candidates int
	Skipped    int
	Settling   int
	Failed     int
	Reasons    []ReasonCount
}

// SavePrescan replaces the single stored library count.
func (s *Store) SavePrescan(p Prescan) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	finished := p.Finished
	if finished.IsZero() {
		finished = time.Now()
	}
	if _, err := tx.Exec(`
INSERT INTO prescan (id, finished_unix, files, candidates, skipped, settling, failed)
VALUES (1, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  finished_unix=excluded.finished_unix,
  files=excluded.files,
  candidates=excluded.candidates,
  skipped=excluded.skipped,
  settling=excluded.settling,
  failed=excluded.failed`,
		finished.Unix(), p.Files, p.Candidates, p.Skipped, p.Settling, p.Failed); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM prescan_reasons`); err != nil {
		return err
	}
	for _, rc := range p.Reasons {
		if rc.N == 0 || rc.Reason == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO prescan_reasons (reason, n) VALUES (?, ?)`, rc.Reason, rc.N); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadPrescan returns the stored library count.
func (s *Store) LoadPrescan() (Prescan, bool, error) {
	var p Prescan
	var unix int64
	err := s.db.QueryRow(`SELECT finished_unix, files, candidates, skipped, settling, failed FROM prescan WHERE id = 1`).
		Scan(&unix, &p.Files, &p.Candidates, &p.Skipped, &p.Settling, &p.Failed)
	if err == sql.ErrNoRows {
		return Prescan{}, false, nil
	}
	if err != nil {
		return Prescan{}, false, err
	}
	p.Finished = time.Unix(unix, 0)
	rows, err := s.db.Query(`SELECT reason, n FROM prescan_reasons ORDER BY n DESC, reason`)
	if err != nil {
		return Prescan{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var rc ReasonCount
		if err := rows.Scan(&rc.Reason, &rc.N); err != nil {
			return Prescan{}, false, err
		}
		p.Reasons = append(p.Reasons, rc)
	}
	return p, true, rows.Err()
}
