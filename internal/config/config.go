// Package config resolves the on-disk locations hidage uses and loads the user's
// settings file. The node identity, settings and friend list live under XDG
// config (~/.config/hidage); the bulk chunk data lives in a separate node-wide
// data store (by default under XDG data, ~/.local/share/hidage/store), which
// should sit on a roomy disk excluded from backups.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

// AppName is the directory name used under the user's config and data roots.
const AppName = "hidage"

// LegacyDataStore is the data store location used before the default moved under
// XDG data. It is still used when it exists, so existing stores keep working.
const LegacyDataStore = "/opt/hidage/store"

// DataStoreEnv overrides the data store location (and the config file's store).
const DataStoreEnv = "HIDAGE_STORE"

// Paths describes node-level config locations (small; safe to keep in $HOME).
type Paths struct {
	// Dir is the root config directory (e.g. ~/.config/hidage).
	Dir string
	// ConfigPath is the user's settings file.
	ConfigPath string
	// KeyringPath is where the wrapped root secret (master key + identity) lives.
	KeyringPath string
	// TrustPath is the legacy trust policy file, imported into the friend list.
	TrustPath string
	// FriendsPath is the node's friend list (address book).
	FriendsPath string
	// ReputationPath is the node's per-peer audit history.
	ReputationPath string
	// AccountingPath is the node's per-peer space ledger.
	AccountingPath string
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
		ConfigPath:     filepath.Join(dir, "config.toml"),
		KeyringPath:    filepath.Join(dir, "keyring.json"),
		TrustPath:      filepath.Join(dir, "trust.json"),
		FriendsPath:    filepath.Join(dir, "friends.json"),
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

// Settings is the content of config.toml. Every field has a default, so a
// missing file (or a missing key) behaves like the commented template.
type Settings struct {
	// Name is the nickname suggested to friends in this node's contact card.
	Name string `toml:"name"`
	// Store is the data store directory ("" = default resolution).
	Store string `toml:"store"`
	// Listen is the list of multiaddrs the node listens on.
	Listen  []string `toml:"listen"`
	Hosting Hosting  `toml:"hosting"`
	Backup  Backup   `toml:"backup"`
}

// Hosting configures what this node stores for friends.
type Hosting struct {
	// DefaultGive is the quota granted to a newly added friend.
	DefaultGive Size `toml:"default_give"`
	// LeaseDays is how long a friend's data is kept without being renewed.
	LeaseDays int `toml:"lease_days"`
}

// Backup configures how this node publishes its own backups.
type Backup struct {
	// Redundancy is shards emitted per shard needed (e.g. 2.0 tolerates losing half).
	Redundancy float64 `toml:"redundancy"`
	// MinFriends refuses to publish to fewer reachable friends than this.
	MinFriends int `toml:"min_friends"`
}

// Defaults returns the settings used when config.toml is absent.
func Defaults() Settings {
	name, _ := os.Hostname()
	return Settings{
		Name:    name,
		Listen:  []string{"/ip4/0.0.0.0/tcp/4001"},
		Hosting: Hosting{DefaultGive: 100 << 30, LeaseDays: 90},
		Backup:  Backup{Redundancy: 2.0, MinFriends: 1},
	}
}

var (
	loadOnce sync.Once
	loaded   Settings
	loadErr  error
)

// Load reads config.toml (once per process) over the defaults. A missing file is
// not an error.
func Load() (Settings, error) {
	loadOnce.Do(func() { loaded, loadErr = load() })
	return loaded, loadErr
}

func load() (Settings, error) {
	s := Defaults()
	p, err := Resolve()
	if err != nil {
		return s, err
	}
	if _, err := toml.DecodeFile(p.ConfigPath, &s); err != nil && !errors.Is(err, os.ErrNotExist) {
		return s, fmt.Errorf("read %s: %w", p.ConfigPath, err)
	}
	return s, nil
}

// DataStoreDir returns the node-wide chunk store path: $HIDAGE_STORE if set,
// else the config file's store, else the legacy /opt location if it exists,
// else $XDG_DATA_HOME/hidage/store.
func DataStoreDir() string {
	if v := os.Getenv(DataStoreEnv); v != "" {
		return v
	}
	if s, _ := Load(); s.Store != "" {
		return expandHome(s.Store)
	}
	if _, err := os.Stat(LegacyDataStore); err == nil {
		return LegacyDataStore
	}
	return defaultDataStore()
}

func defaultDataStore() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), AppName, "store")
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, AppName, "store")
}

// expandHome expands a leading "~/" to the user's home directory.
func expandHome(p string) string {
	if len(p) >= 2 && p[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// WriteTemplate writes a commented config.toml (with store set to the resolved
// data store) if none exists yet, returning whether it wrote one.
func WriteTemplate(p Paths) (bool, error) {
	if _, err := os.Stat(p.ConfigPath); err == nil {
		return false, nil
	}
	d := Defaults()
	body := fmt.Sprintf(template, d.Name, DataStoreDir(), d.Listen[0],
		FormatSize(int64(d.Hosting.DefaultGive)), d.Hosting.LeaseDays,
		d.Backup.Redundancy, d.Backup.MinFriends)
	if err := os.WriteFile(p.ConfigPath, []byte(body), 0o600); err != nil {
		return false, fmt.Errorf("write %s: %w", p.ConfigPath, err)
	}
	return true, nil
}

const template = `# hidage settings. Every key is optional; the values shown are the defaults.

# Nickname suggested to friends in your contact card (see "hidage id").
name = %q

# Where chunk data (yours and friends') is stored. HIDAGE_STORE overrides it.
store = %q

# Addresses this node listens on.
listen = [%q]

[hosting]
# Space granted to a newly added friend (change per friend with "hidage friend set").
default_give = %q
# A friend's data not renewed for this many days is reclaimed.
lease_days = %d

[backup]
# Shards emitted per shard needed: 2.0 survives losing half of them.
redundancy = %.1f
# Refuse to publish to fewer reachable friends than this.
min_friends = %d
`
