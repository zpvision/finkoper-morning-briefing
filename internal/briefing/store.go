package briefing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("idempotency_conflict")
var ErrFull = errors.New("queue_full")

type Store struct {
	db    *sql.DB
	lock  *flock.Flock
	mu    sync.Mutex
	limit int
}

func Open(path string) (*Store, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Resolve parent symlinks so common alternate paths share the same lock.
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	path = filepath.Join(parent, filepath.Base(path))
	if fi, e := os.Lstat(path); e == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("database symlinks are unsupported")
	}
	lock := flock.New(path + ".lock")
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("database already locked by another process")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		_ = lock.Unlock()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, lock: lock, limit: QueueLimit}
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS jobs (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, company TEXT NOT NULL,
 idem TEXT NOT NULL, hash TEXT NOT NULL, input BLOB NOT NULL, status TEXT NOT NULL,
 created TEXT NOT NULL, prompt_version TEXT NOT NULL, schema_version TEXT NOT NULL,
 result BLOB, error TEXT, metrics BLOB NOT NULL, UNIQUE(company,idem));
 UPDATE jobs SET status='queued' WHERE status='running';`)
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() { _ = s.db.Close(); _ = s.lock.Unlock() }
func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var created string
	var result, metrics []byte
	var msg sql.NullString
	err := row.Scan(&j.ID, &j.Status, &created, &j.PromptVersion, &j.SchemaVersion, &result, &msg, &metrics)
	if err != nil {
		return j, err
	}
	j.Created, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return j, err
	}
	if result != nil {
		if err = json.Unmarshal(result, &j.Result); err != nil {
			return j, err
		}
	}
	if msg.Valid {
		j.Error = &msg.String
	}
	err = json.Unmarshal(metrics, &j.Metrics)
	return j, err
}

const fields = "id,status,created,prompt_version,schema_version,result,error,metrics"

func (s *Store) Lookup(ctx context.Context, in Input, key string) (Job, error) {
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	var id, hash string
	err := s.db.QueryRowContext(ctx, "SELECT id,hash FROM jobs WHERE company=? AND idem=?", in.CompanyID, key).Scan(&id, &hash)
	if err != nil {
		return Job{}, err
	}
	if hash != hex.EncodeToString(sum[:]) {
		return Job{}, ErrConflict
	}
	return s.Get(ctx, in.CompanyID, id)
}

func (s *Store) Get(ctx context.Context, company, id string) (Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, "SELECT "+fields+" FROM jobs WHERE company=? AND id=?", company, id))
}
func (s *Store) Enqueue(ctx context.Context, in Input, key string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var oldID, oldHash string
	err = tx.QueryRowContext(ctx, "SELECT id,hash FROM jobs WHERE company=? AND idem=?", in.CompanyID, key).Scan(&oldID, &oldHash)
	if err == nil {
		if hash != oldHash {
			return Job{}, ErrConflict
		}
		j, e := scanJob(tx.QueryRowContext(ctx, "SELECT "+fields+" FROM jobs WHERE id=?", oldID))
		return j, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE status IN ('queued','running')").Scan(&count); err != nil {
		return Job{}, err
	}
	if count >= s.limit {
		return Job{}, ErrFull
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return Job{}, err
	}
	j := Job{ID: hex.EncodeToString(id[:]), Status: "queued", Created: time.Now().UTC(), PromptVersion: PromptVersion, SchemaVersion: SchemaVersion}
	metrics, _ := json.Marshal(j.Metrics)
	_, err = tx.ExecContext(ctx, "INSERT INTO jobs (id,company,idem,hash,input,status,created,prompt_version,schema_version,metrics) VALUES (?,?,?,?,?,?,?,?,?,?)", j.ID, in.CompanyID, key, hash, b, j.Status, j.Created.Format(time.RFC3339Nano), PromptVersion, SchemaVersion, metrics)
	if err != nil {
		return Job{}, err
	}
	return j, tx.Commit()
}
func (s *Store) claim(ctx context.Context) (Job, Input, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, Input{}, err
	}
	defer tx.Rollback()
	j, err := scanJob(tx.QueryRowContext(ctx, "SELECT "+fields+" FROM jobs WHERE status='queued' ORDER BY seq LIMIT 1"))
	if err != nil {
		return j, Input{}, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT input FROM jobs WHERE id=?", j.ID).Scan(&raw); err != nil {
		return j, Input{}, err
	}
	var in Input
	if err = json.Unmarshal(raw, &in); err != nil {
		return j, in, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE jobs SET status='running' WHERE id=?", j.ID)
	if err != nil {
		return j, in, err
	}
	return j, in, tx.Commit()
}
func (s *Store) finish(ctx context.Context, j Job) error {
	var result any
	if j.Result != nil {
		b, _ := json.Marshal(j.Result)
		result = b
	}
	metrics, _ := json.Marshal(j.Metrics)
	_, err := s.db.ExecContext(ctx, "UPDATE jobs SET status=?,result=?,error=?,metrics=? WHERE id=?", j.Status, result, j.Error, metrics, j.ID)
	return err
}
