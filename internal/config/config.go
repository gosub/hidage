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
	"strings"
	"sync"
	"time"

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
	// InvitesPath holds pending invitations (hashed tokens).
	InvitesPath string
	// IdentityPath caches the identity key so the daemon can host for friends
	// without the passphrase (see keyring.SaveIdentity).
	IdentityPath string
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
		Dir:          dir,
		ConfigPath:   filepath.Join(dir, "config.toml"),
		KeyringPath:  filepath.Join(dir, "keyring.json"),
		TrustPath:    filepath.Join(dir, "trust.json"),
		FriendsPath:  filepath.Join(dir, "friends.json"),
		InvitesPath:  filepath.Join(dir, "invites.json"),
		IdentityPath: filepath.Join(dir, "identity.key"),
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
	Listen []string `toml:"listen"`
	// Reachability is "auto", "public" or "private" (see p2p.Reachability).
	Reachability string `toml:"reachability"`
	// PassphraseCommand, if set, is run (via sh -c) to obtain the keyring
	// passphrase on stdout, e.g. for an unattended daemon.
	PassphraseCommand string  `toml:"passphrase_command"`
	Hosting           Hosting `toml:"hosting"`
	Backup            Backup  `toml:"backup"`
}

// Hosting configures what this node stores for friends.
type Hosting struct {
	// DefaultGive is the quota granted to a newly added friend.
	DefaultGive Size `toml:"default_give"`
	// LeaseDays is how long a friend's backup is kept without being renewed.
	LeaseDays int `toml:"lease_days"`
	// TrashDays is how long data a friend replaced or dropped is kept before
	// it is deleted (a guard against a compromised friend machine wiping it).
	TrashDays int `toml:"trash_days"`
}

// Backup configures how this node publishes its own backups.
type Backup struct {
	// Redundancy is shards emitted per shard needed (e.g. 2.0 tolerates losing half).
	Redundancy float64 `toml:"redundancy"`
	// MinFriends refuses to publish to fewer reachable friends than this.
	MinFriends int `toml:"min_friends"`
	// Folders are backed up on schedule by "hidage daemon".
	Folders []string `toml:"folders"`
	// Every is how often the daemon snapshots folders and publishes changes.
	Every Interval `toml:"every"`
	// RenewEvery re-publishes unchanged folders so friends keep them.
	RenewEvery Interval `toml:"renew_every"`
	// AuditEvery is how often to check that friends still hold the data.
	AuditEvery Interval `toml:"audit_every"`
}

// FolderPaths returns the configured folders with "~/" expanded.
func (b Backup) FolderPaths() []string {
	out := make([]string, len(b.Folders))
	for i, f := range b.Folders {
		out[i] = expandHome(f)
	}
	return out
}

// Defaults returns the settings used when config.toml is absent.
func Defaults() Settings {
	name, _ := os.Hostname()
	return Settings{
		Name: name,
		Listen: []string{
			"/ip4/0.0.0.0/tcp/4001", "/ip4/0.0.0.0/udp/4001/quic-v1",
			"/ip6/::/tcp/4001", "/ip6/::/udp/4001/quic-v1",
		},
		Reachability: "auto",
		Hosting:      Hosting{DefaultGive: 100 << 30, LeaseDays: 90, TrashDays: 30},
		Backup: Backup{
			Redundancy: 2.0, MinFriends: 1, Folders: []string{},
			Every: Interval(time.Hour), RenewEvery: Interval(24 * time.Hour), AuditEvery: Interval(24 * time.Hour),
		},
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
	p, err := Resolve()
	if err != nil {
		return Defaults(), err
	}
	return loadFile(p.ConfigPath)
}

func loadFile(path string) (Settings, error) {
	s := Defaults()
	if _, err := toml.DecodeFile(path, &s); err != nil && !errors.Is(err, os.ErrNotExist) {
		return s, fmt.Errorf("read %s: %w", path, err)
	}
	return s, nil
}

// reset forgets the cached settings, so the next Load re-reads the file.
func reset() { loadOnce = sync.Once{} }

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
	listen := make([]string, len(d.Listen))
	for i, l := range d.Listen {
		listen[i] = fmt.Sprintf("%q", l)
	}
	body := fmt.Sprintf(template, d.Name, DataStoreDir(), strings.Join(listen, ", "), d.Reachability,
		FormatSize(int64(d.Hosting.DefaultGive)), d.Hosting.LeaseDays, d.Hosting.TrashDays,
		d.Backup.Redundancy, d.Backup.MinFriends, d.Backup.Every, d.Backup.RenewEvery, d.Backup.AuditEvery)
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

# Addresses this node listens on (TCP and QUIC, IPv4 and IPv6).
listen = [%s]

# Can friends reach this node from the internet?
#   "auto"    public if a network interface has a public IP, else behind a NAT
#   "public"  e.g. a port is forwarded: this node relays for friends behind NATs
#   "private" behind a NAT: reserve a slot on a reachable friend's relay
reachability = %q

# Command printing the keyring passphrase, so "hidage daemon" can take backups
# unattended, e.g. "systemd-creds decrypt ~/.config/hidage/pass.cred -" or
# "pass show hidage". Empty: ask on the terminal (or use HIDAGE_PASSPHRASE).
passphrase_command = ""

[hosting]
# Space granted to a newly added friend (change per friend with "hidage friend set").
default_give = %q
# A friend's backup not renewed for this many days is dropped.
lease_days = %d
# Data a friend replaced or dropped is kept this many days before deletion, so a
# compromised friend machine can't wipe its backups at once.
trash_days = %d

[backup]
# Shards emitted per shard needed: 2.0 survives losing half of them.
redundancy = %.1f
# Refuse to publish to fewer reachable friends than this.
min_friends = %d
# Folders "hidage daemon" backs up on schedule, e.g. ["~/Documents", "~/projects"].
folders = []
# How often the daemon snapshots them and publishes what changed.
every = %q
# Re-publish unchanged folders this often, so friends keep them (see lease_days).
renew_every = %q
# How often to check that friends still hold your data.
audit_every = %q
`
