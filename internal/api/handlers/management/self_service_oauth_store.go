package management

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/fileperm"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// The single writer is the server process, including all its management handlers.
// Pending credentials live outside AuthDir so neither the watcher nor a restart
// can register or refresh them before an administrator accepts them.
var selfServiceMu sync.Mutex

const selfServiceRecordLimit = 5000

type selfServiceRecord struct {
	RenewalOf       string                   `json:"renewal_of,omitempty"`
	RenewalRevision string                   `json:"renewal_revision,omitempty"`
	Console         *coreauth.ConsoleBinding `json:"console_binding,omitempty"`
	ID              string                   `json:"id"`
	Provider        string                   `json:"provider"`
	Label           string                   `json:"label"`
	Status          string                   `json:"status"`
	CreatedAt       time.Time                `json:"created_at"`
	ExpiresAt       time.Time                `json:"expires_at"`
	FlowExpiresAt   time.Time                `json:"flow_expires_at,omitempty"`
	ExchangeAt      time.Time                `json:"exchange_at,omitempty"`
	ConsentAt       *time.Time               `json:"consent_at,omitempty"`
	SubmittedAt     *time.Time               `json:"submitted_at,omitempty"`
	ReviewedAt      *time.Time               `json:"reviewed_at,omitempty"`
	TokenHash       string                   `json:"token_hash"`
	State           string                   `json:"state,omitempty"`
	Verifier        string                   `json:"verifier,omitempty"`
	Challenge       string                   `json:"challenge,omitempty"`
	AuthURL         string                   `json:"auth_url,omitempty"`
	Credentials     map[string]any           `json:"credentials,omitempty"`
	FileName        string                   `json:"file_name,omitempty"`
	Email           string                   `json:"email,omitempty"`
	AccountID       string                   `json:"account_id,omitempty"`
}

func selfServiceRandom(bytes int) (string, error) {
	data := make([]byte, bytes)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func selfServiceID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 16 && strings.ToLower(id) == id
}

func (h *Handler) selfServiceDir() (string, error) {
	cfg := h.snapshotAuthFileRuntime().cfg
	if strings.TrimSpace(cfg.AuthDir) == "" {
		return "", errors.New("auth directory unavailable")
	}
	dir, err := util.ResolveAuthDir(cfg.AuthDir)
	if err != nil {
		return "", err
	}
	dir, err = filepath.Abs(dir)
	if err != nil || filepath.Dir(dir) == dir {
		return "", errors.New("invalid auth directory")
	}
	return dir + ".self-service-oauth", nil
}

func selfServiceRead(dir, id string) (*selfServiceRecord, error) {
	if !selfServiceID(id) {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil, err
	}
	var record selfServiceRecord
	if err = json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	if record.ID != id {
		return nil, errors.New("record identity mismatch")
	}
	return &record, nil
}

func selfServiceWrite(dir string, record *selfServiceRecord) error {
	if !selfServiceID(record.ID) {
		return errors.New("invalid record identity")
	}
	if err := fileperm.EnsurePrivateDir(dir); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".intake-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, record.ID+".json"))
}

func selfServiceList(dir string) ([]*selfServiceRecord, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []*selfServiceRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	records := make([]*selfServiceRecord, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := selfServiceRead(dir, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, fmt.Errorf("read intake record: %w", err)
		}
		records = append(records, record)
	}
	return records, nil
}

func selfServiceAuthenticate(dir, token string) (*selfServiceRecord, error) {
	id, secret, ok := strings.Cut(token, ".")
	if !ok || len(secret) != 64 {
		return nil, os.ErrNotExist
	}
	record, err := selfServiceRead(dir, id)
	if err != nil {
		return nil, os.ErrNotExist
	}
	digest := sha256.Sum256([]byte(secret))
	expected, err := hex.DecodeString(record.TokenHash)
	if err != nil || subtle.ConstantTimeCompare(expected, digest[:]) != 1 {
		return nil, os.ErrNotExist
	}
	if record.Status == "revoked" || !time.Now().Before(record.ExpiresAt) {
		return nil, os.ErrNotExist
	}
	return record, nil
}

func (r *selfServiceRecord) clearFlow() {
	r.State = ""
	r.Verifier = ""
	r.Challenge = ""
	r.AuthURL = ""
}

func (r *selfServiceRecord) invitationStatus() string {
	if (r.Status == "invited" || r.Status == "authorizing") && !time.Now().Before(r.ExpiresAt) {
		return "expired"
	}
	return r.Status
}

func (r *selfServiceRecord) canDeleteInvitation() bool {
	status := r.invitationStatus()
	return (status == "expired" || status == "revoked") && r.FileName == "" && len(r.Credentials) == 0
}
