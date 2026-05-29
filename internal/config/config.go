// Package config resolves the on-disk locations hidage uses. The node identity
// lives under XDG config (~/.config/hidage); the bulk chunk data lives in a
// separate node-wide data store outside $HOME so it doesn't bloat a backed-up
// home directory.
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// AppName is the directory name used under the user's config root.
const AppName = "hidage"

// DefaultDataStore is where chunk objects go when HIDAGE_STORE is unset. It is
// intentionally outside $HOME — the store grows large (your data plus, later,
// other peers' shards) and should sit on a roomy disk excluded from backups.
const DefaultDataStore = "/opt/hidage/store"

// DataStoreEnv overrides the data store location.
const DataStoreEnv = "HIDAGE_STORE"

// Paths describes node-level config locations (small; safe to keep in $HOME).
type Paths struct {
	// Dir is the root config directory (e.g. ~/.config/hidage).
	Dir string
	// KeyringPath is where the wrapped root secret (master key + identity) lives.
	KeyringPath string
	// TrustPath is the node's trust/whitelist policy file.
	TrustPath string
	// ReputationPath is the node's per-peer audit history.
	ReputationPath string
	// AccountingPath is the node's per-peer space ledger.
	AccountingPath string
}

// DataStoreDir returns the node-wide chunk store path: $HIDAGE_STORE if set,
// otherwise DefaultDataStore.
func DataStoreDir() string {
	if v := os.Getenv(DataStoreEnv); v != "" {
		return v
	}
	return DefaultDataStore
}

// Resolve computes the config paths without creating anything on disk.
func Resolve() (Paths, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	dir := filepath.Join(base, AppName)
	return Paths{
		Dir:            dir,
		KeyringPath:    filepath.Join(dir, "keyring.json"),
		TrustPath:      filepath.Join(dir, "trust.json"),
		ReputationPath: filepath.Join(dir, "reputation.json"),
		AccountingPath: filepath.Join(dir, "accounting.json"),
	}, nil
}

// Ensure resolves the paths and creates the config directory.
func Ensure() (Paths, error) {
	p, err := Resolve()
	if err != nil {
		return Paths{}, err
	}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return Paths{}, fmt.Errorf("create config dir: %w", err)
	}
	return p, nil
}
