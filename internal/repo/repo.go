// Package repo implements hidage's versioned storage: a repository holds a
// content-addressed store of deterministically-encrypted objects plus a set of
// signed snapshots. Taking a snapshot chunks the source tree into a Merkle DAG
// and stores only the objects not already present, so updates re-store just the
// delta. Old snapshots remain fully restorable; prune garbage-collects objects
// no snapshot references.
package repo

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"hidage/internal/crypto"
	"hidage/internal/store"
)

const configName = "hidage-repo.json"

// Config is the on-disk repository descriptor. The master key is wrapped under a
// passphrase-derived KEK; only the wrapped form is ever written.
type Config struct {
	Version          int                 `json:"version"`
	RepoID           string              `json:"repoID"`
	Argon2           crypto.Argon2Params `json:"argon2"`
	WrappedMasterKey []byte              `json:"wrappedMasterKey"`
}

// Repo is an opened repository.
type Repo struct {
	Path      string
	cfg       Config
	store     *store.FSStore
	masterKey []byte
}

func configPath(path string) string   { return filepath.Join(path, configName) }
func storeDir(path string) string     { return filepath.Join(path, "store") }
func snapshotsDir(path string) string { return filepath.Join(path, "snapshots") }

// Exists reports whether a repository is present at path.
func Exists(path string) bool {
	_, err := os.Stat(configPath(path))
	return err == nil
}

// Init creates a new repository at path, generating a fresh master key wrapped
// under the passphrase. It fails if a repository already exists there.
func Init(path, passphrase string) (*Repo, error) {
	if Exists(path) {
		return nil, fmt.Errorf("repository already exists at %s", path)
	}
	for _, d := range []string{path, storeDir(path), snapshotsDir(path)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}

	argon, err := crypto.DefaultArgon2Params()
	if err != nil {
		return nil, err
	}
	masterKey, err := crypto.NewDEK()
	if err != nil {
		return nil, err
	}
	kek := crypto.DeriveKEK(passphrase, argon)
	wrapped, err := crypto.WrapDEK(kek, masterKey)
	if err != nil {
		return nil, err
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}

	cfg := Config{
		Version:          1,
		RepoID:           hex.EncodeToString(idBytes),
		Argon2:           argon,
		WrappedMasterKey: wrapped,
	}
	if err := writeConfig(path, cfg); err != nil {
		return nil, err
	}
	st, err := store.NewFSStore(storeDir(path))
	if err != nil {
		return nil, err
	}
	return &Repo{Path: path, cfg: cfg, store: st, masterKey: masterKey}, nil
}

// Open unlocks an existing repository with the passphrase.
func Open(path, passphrase string) (*Repo, error) {
	if !Exists(path) {
		return nil, fmt.Errorf("no repository at %s (run `hidage repo init`)", path)
	}
	data, err := os.ReadFile(configPath(path))
	if err != nil {
		return nil, fmt.Errorf("read repo config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse repo config: %w", err)
	}
	kek := crypto.DeriveKEK(passphrase, cfg.Argon2)
	masterKey, err := crypto.UnwrapDEK(kek, cfg.WrappedMasterKey)
	if err != nil {
		return nil, errors.New("cannot unlock repository (wrong passphrase?)")
	}
	st, err := store.NewFSStore(storeDir(path))
	if err != nil {
		return nil, err
	}
	return &Repo{Path: path, cfg: cfg, store: st, masterKey: masterKey}, nil
}

// Store exposes the underlying object store (used by info/verify-style callers).
func (r *Repo) Store() *store.FSStore { return r.store }

func writeConfig(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(path), data, 0o600)
}
