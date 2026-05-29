// Package config resolves the on-disk locations hidage uses for its identity
// key and local shard store. Paths follow the XDG Base Directory spec, falling
// back to ~/.config/hidage when XDG_CONFIG_HOME is unset.
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// AppName is the directory name used under the user's config root.
const AppName = "hidage"

// Paths describes the resolved filesystem layout for a hidage installation.
type Paths struct {
	// Dir is the root config directory (e.g. ~/.config/hidage).
	Dir string
	// IdentityPath is where the Ed25519 private key is stored.
	IdentityPath string
	// StoreDir is the root of the content-addressed shard store.
	StoreDir string
}

// Resolve computes the paths without creating anything on disk.
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
		Dir:          dir,
		IdentityPath: filepath.Join(dir, "identity.key"),
		StoreDir:     filepath.Join(dir, "store"),
	}, nil
}

// Ensure resolves the paths and creates the config and store directories.
func Ensure() (Paths, error) {
	p, err := Resolve()
	if err != nil {
		return Paths{}, err
	}
	if err := os.MkdirAll(p.StoreDir, 0o700); err != nil {
		return Paths{}, fmt.Errorf("create store dir: %w", err)
	}
	return p, nil
}
