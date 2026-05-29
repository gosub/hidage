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
	// IdentityPath is where the Ed25519 private key is stored.
	IdentityPath string
	// MasterKeyPath is where the wrapped user-level encryption key is stored.
	MasterKeyPath string
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
		Dir:           dir,
		IdentityPath:  filepath.Join(dir, "identity.key"),
		MasterKeyPath: filepath.Join(dir, "masterkey.json"),
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
