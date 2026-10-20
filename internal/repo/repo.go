// Package repo implements hidage's versioned storage. A repository is a tracked
// folder containing a .hidage/ directory (like .git): it holds the repo marker,
// the small Merkle-DAG structure nodes, and the signed snapshots. The bulk chunk
// data lives in a separate node-wide store outside the folder, and the
// encryption master key is user-level (see internal/keyring), so the same chunk
// deduplicates across every folder you back up. Taking a snapshot stores only
// the objects not already present; old snapshots stay restorable until pruned.
package repo

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"hidage/internal/store"
)

// hidageDir is the per-folder metadata directory name.
const hidageDir = ".hidage"

// Config marks a folder as a hidage repository. It holds no key material — the
// encryption key is user-level.
type Config struct {
	Version int    `json:"version"`
	RepoID  string `json:"repoID"`
}

// Repo is an opened repository rooted at a tracked folder.
type Repo struct {
	Folder    string // the tracked folder (contains .hidage/)
	repoID    string
	meta      store.Store
	data      store.Store
	masterKey []byte
}

// RepoID returns this repository's stable identifier (from .hidage/config.json).
func (r *Repo) RepoID() string { return r.repoID }

// HidageFile returns the path of an auxiliary file inside this repo's .hidage/
// directory (e.g. the distribution record written by publish).
func (r *Repo) HidageFile(name string) string {
	return filepath.Join(r.Folder, hidageDir, name)
}

func metaDir(folder string) string    { return filepath.Join(folder, hidageDir, "objects") }
func snapsDir(folder string) string   { return filepath.Join(folder, hidageDir, "snapshots") }
func configPath(folder string) string { return filepath.Join(folder, hidageDir, "config.json") }

// Exists reports whether folder is a hidage repository.
func Exists(folder string) bool {
	_, err := os.Stat(configPath(folder))
	return err == nil
}

// Find walks up from start looking for a repository, returning the tracked
// folder, or "" if none is found.
func Find(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		if Exists(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// openDataStore opens the node-wide chunk store for a repository (namespaced by
// repoID, deduplicated across repos via hard links), turning a permission
// failure into actionable guidance.
func openDataStore(dataDir, repoID string) (store.Store, error) {
	st, err := store.NewHardlinkStore(dataDir, repoID)
	if err == nil {
		return st, nil
	}
	if errors.Is(err, fs.ErrPermission) {
		return nil, fmt.Errorf("cannot use data store at %s: permission denied.\n"+
			"Create it once, e.g.:\n"+
			"  sudo mkdir -p %s && sudo chown \"$USER\" %s\n"+
			"or set `store` in ~/.config/hidage/config.toml (or HIDAGE_STORE) to a writable path.", dataDir, dataDir, dataDir)
	}
	return nil, err
}

// Init creates a repository in folder, with chunk data going to dataDir and
// objects encrypted under masterKey. It fails if folder is already a repo.
func Init(folder string, masterKey []byte, dataDir string) (*Repo, error) {
	folder, err := filepath.Abs(folder)
	if err != nil {
		return nil, err
	}
	if Exists(folder) {
		return nil, fmt.Errorf("%s is already a hidage repository", folder)
	}
	for _, d := range []string{metaDir(folder), snapsDir(folder)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	cfg := Config{Version: 1, RepoID: hex.EncodeToString(idBytes)}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(configPath(folder), data, 0o600); err != nil {
		return nil, fmt.Errorf("write repo config: %w", err)
	}

	meta, err := store.NewFSStore(metaDir(folder))
	if err != nil {
		return nil, err
	}
	ds, err := openDataStore(dataDir, cfg.RepoID)
	if err != nil {
		return nil, err
	}
	return &Repo{Folder: folder, repoID: cfg.RepoID, meta: meta, data: ds, masterKey: masterKey}, nil
}

// Open opens an existing repository in folder.
func Open(folder string, masterKey []byte, dataDir string) (*Repo, error) {
	folder, err := filepath.Abs(folder)
	if err != nil {
		return nil, err
	}
	if !Exists(folder) {
		return nil, fmt.Errorf("%s is not a hidage repository", folder)
	}
	raw, err := os.ReadFile(configPath(folder))
	if err != nil {
		return nil, fmt.Errorf("read repo config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse repo config: %w", err)
	}
	meta, err := store.NewFSStore(metaDir(folder))
	if err != nil {
		return nil, err
	}
	ds, err := openDataStore(dataDir, cfg.RepoID)
	if err != nil {
		return nil, err
	}
	return &Repo{Folder: folder, repoID: cfg.RepoID, meta: meta, data: ds, masterKey: masterKey}, nil
}
