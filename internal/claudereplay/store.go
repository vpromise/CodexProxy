package claudereplay

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	log "github.com/sirupsen/logrus"
)

const (
	refPrefix      = "rs_cpr1_"
	recordTTL      = time.Hour
	maxRecordSize  = 512 << 10
	maxPoolTurns   = 128
	maxLookup      = 64
	maxLookupBytes = 8 << 20
	maxBlocks      = 256
)

const schema = `
CREATE TABLE IF NOT EXISTS codexproxy_claude_replay_v1 (
  id text PRIMARY KEY,
  owner_key text NOT NULL,
  pool_key text NOT NULL,
  expires_at timestamptz NOT NULL,
  tool_keys text[] NOT NULL,
  body bytea NOT NULL
);
CREATE INDEX IF NOT EXISTS codexproxy_claude_replay_v1_expiry ON codexproxy_claude_replay_v1 (expires_at);
CREATE INDEX IF NOT EXISTS codexproxy_claude_replay_v1_pool ON codexproxy_claude_replay_v1 (pool_key, expires_at DESC, id);
CREATE INDEX IF NOT EXISTS codexproxy_claude_replay_v1_tool ON codexproxy_claude_replay_v1 USING gin (tool_keys);
`

// Store keeps encrypted, short-lived Claude Responses history outside the auth store.
type Store struct {
	db     *sql.DB
	pools  map[string]bool
	aead   cipher.AEAD
	macKey []byte
	cancel context.CancelFunc
	done   chan struct{}
}

// Scope comes from the currently authorized request and the selected upstream.
type Scope struct {
	Caller   string
	Pool     string
	Account  string
	Endpoint string
	Model    string
}

type record struct {
	ID           string          `json:"id"`
	Binding      string          `json:"binding"`
	Prefix       string          `json:"prefix"`
	MessageCount int             `json:"message_count"`
	Content      json.RawMessage `json:"content"`
}

// OpenFromEnv enables replay only for the explicitly listed Claude pools.
func OpenFromEnv(ctx context.Context) (*Store, error) {
	pools := strings.TrimSpace(os.Getenv("CODEXPROXY_CLAUDE_REPLAY_POOLS"))
	if pools == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("CODEXPROXY_CLAUDE_REPLAY_KEY")))
	if err != nil || len(key) != 32 {
		return nil, errors.New("CODEXPROXY_CLAUDE_REPLAY_KEY must encode exactly 32 bytes in base64")
	}
	defer clear(key)
	return open(ctx, strings.TrimSpace(os.Getenv("CODEXPROXY_CLAUDE_REPLAY_POSTGRES_DSN")), key, strings.Split(pools, ","))
}

func newStore(key []byte, pools []string) (*Store, error) {
	if len(key) != 32 {
		return nil, errors.New("Claude replay requires a 256-bit key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	optedIn := make(map[string]bool, len(pools))
	for _, pool := range pools {
		pool = strings.TrimSpace(pool)
		if pool != "" {
			optedIn[pool] = true
		}
	}
	if len(optedIn) == 0 {
		return nil, errors.New("Claude replay requires at least one explicitly named pool")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("codexproxy claude responses replay v1 reference key"))
	return &Store{pools: optedIn, aead: aead, macKey: mac.Sum(nil)}, nil
}

func open(ctx context.Context, dsn string, key []byte, pools []string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("CODEXPROXY_CLAUDE_REPLAY_POSTGRES_DSN is required for replay")
	}
	s, err := newStore(key, pools)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, errors.New("invalid Claude replay PostgreSQL configuration")
	}
	s.db = db
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)

	if err := s.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, errors.New("Claude replay PostgreSQL initialization failed")
	}

	worker, stop := context.WithCancel(context.Background())
	s.cancel, s.done = stop, make(chan struct{})
	go s.sweep(worker)
	return s, nil
}

func (s *Store) ensureSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('codexproxy_claude_replay_v1_schema', 0))"); err != nil {
		return err
	}
	for _, statement := range strings.Split(strings.TrimSpace(schema), ";\n") {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Close stops expiry cleanup and releases only this feature's connection pool.
func (s *Store) Close() {
	if s == nil {
		return
	}
	s.cancel()
	<-s.done
	if err := s.db.Close(); err != nil {
		log.Warn("Claude replay PostgreSQL close failed")
	}
}

func (s *Store) sweep(ctx context.Context) {
	defer close(s.done)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		_, err := s.db.ExecContext(ctx, "DELETE FROM codexproxy_claude_replay_v1 WHERE expires_at <= CURRENT_TIMESTAMP")
		if err != nil && ctx.Err() == nil {
			log.Warn("Claude replay expiration cleanup failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Store) enabled(scope Scope) bool {
	return s != nil && s.pools[scope.Pool] && scope.Caller != "" && scope.Account != "" && scope.Model != "" && scope.Endpoint != ""
}

func (s *Store) key(fields ...string) []byte {
	mac := hmac.New(sha256.New, s.macKey)
	for _, field := range fields {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		mac.Write(size[:])
		mac.Write([]byte(field))
	}
	return mac.Sum(nil)
}

func (s *Store) keyText(fields ...string) string {
	return hex.EncodeToString(s.key(fields...))
}

func (s *Store) owner(scope Scope) string {
	return s.keyText("owner", scope.Pool, scope.Caller)
}

func (s *Store) binding(scope Scope) string {
	return s.keyText("upstream", scope.Account, scope.Endpoint, scope.Model)
}

func (s *Store) ref(scope Scope, id string, index int) string {
	if index < 0 || index >= maxBlocks {
		return ""
	}
	block := make([]byte, 18, 34)
	copy(block, id)
	binary.BigEndian.PutUint16(block[16:], uint16(index))
	tag := s.key("reference", s.owner(scope), string(block))
	return refPrefix + base64.RawURLEncoding.EncodeToString(append(block, tag[:16]...))
}

func (s *Store) parseRef(scope Scope, ref string) (id string, index int, valid bool) {
	encoded, ok := strings.CutPrefix(ref, refPrefix)
	if !ok || len(encoded) != base64.RawURLEncoding.EncodedLen(34) {
		return "", 0, false
	}
	block, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return "", 0, false
	}
	tag := s.key("reference", s.owner(scope), string(block[:18]))
	index = int(binary.BigEndian.Uint16(block[16:18]))
	if index >= maxBlocks || !hmac.Equal(block[18:], tag[:16]) {
		return "", 0, false
	}
	return string(block[:16]), index, true
}

func (s *Store) seal(id, owner string, expires time.Time, data []byte) []byte {
	return s.aead.Seal(nil, nil, data, s.key("row", id, owner, strconv.FormatInt(expires.UnixMicro(), 10)))
}

func (s *Store) unseal(id, owner string, expires time.Time, data []byte) ([]byte, error) {
	return s.aead.Open(nil, nil, data, s.key("row", id, owner, strconv.FormatInt(expires.UnixMicro(), 10)))
}

func (s *Store) save(ctx context.Context, scope Scope, id string, value record, calls []string) error {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxRecordSize {
		return errors.New("Claude replay record exceeds its storage bound")
	}
	owner, pool := s.owner(scope), s.keyText("pool", scope.Pool)
	key := s.keyText("record", owner, id)
	expires := time.Now().UTC().Truncate(time.Microsecond).Add(recordTTL)
	encrypted := s.seal(key, owner, expires, encoded)
	clear(encoded)

	tools := make([]string, 0, len(calls))
	for _, call := range calls {
		tools = append(tools, s.keyText("tool", owner, call))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", pool); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO codexproxy_claude_replay_v1 (id, owner_key, pool_key, expires_at, tool_keys, body)
VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (id) DO NOTHING`, key, owner, pool, expires, tools, encrypted); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM codexproxy_claude_replay_v1 WHERE id IN
  (SELECT id FROM codexproxy_claude_replay_v1 WHERE pool_key = $1 ORDER BY expires_at DESC, id OFFSET $2)`, pool, maxPoolTurns); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) find(ctx context.Context, scope Scope, ids, calls []string) ([]record, error) {
	owner := s.owner(scope)
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, s.keyText("record", owner, id))
	}
	tools := make([]string, 0, len(calls))
	for _, call := range calls {
		tools = append(tools, s.keyText("tool", owner, call))
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, expires_at, body FROM codexproxy_claude_replay_v1
WHERE owner_key = $1 AND expires_at > CURRENT_TIMESTAMP AND (id = ANY($2::text[]) OR tool_keys && $3::text[])
LIMIT $4`, owner, keys, tools, maxLookup+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make([]record, 0)
	totalBytes := 0
	for rows.Next() {
		var key string
		var expires time.Time
		var data []byte
		if err := rows.Scan(&key, &expires, &data); err != nil {
			return nil, err
		}
		totalBytes += len(data)
		if len(found) == maxLookup || totalBytes > maxLookupBytes || len(data) > maxRecordSize+s.aead.Overhead() {
			return nil, errors.New("Claude replay storage bound exceeded")
		}
		decoded, err := s.unseal(key, owner, expires, data)
		if err != nil {
			return nil, errors.New("Claude replay record authentication failed")
		}
		var value record
		err = json.Unmarshal(decoded, &value)
		clear(decoded)
		id, idErr := base64.RawURLEncoding.DecodeString(value.ID)
		if err != nil || idErr != nil || len(id) != 16 || key != s.keyText("record", owner, string(id)) {
			return nil, errors.New("invalid Claude replay record")
		}
		value.ID = string(id)
		found = append(found, value)
	}
	return found, rows.Err()
}

func newID() string {
	var buffer [16]byte
	rand.Read(buffer[:])
	return string(buffer[:])
}

type UnavailableError struct{}

func (UnavailableError) Error() string {
	return `{"error":{"message":"Claude continuation could not be verified. Resend the original conversation or start a new tool turn.","type":"invalid_request_error","code":"claude_replay_unavailable"}}`
}
func (UnavailableError) StatusCode() int       { return 400 }
func (UnavailableError) IsRequestScoped() bool { return true }
