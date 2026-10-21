// Package friends is a node's friend list: the peers it backs up to and hosts
// data for. hidage is friends-only, so this list is the whole trust model: a
// peer not on it cannot connect to hidage protocols, and only friends receive
// shards. Each friend is identified by its Ed25519 public key and carries a local
// nickname, last known addresses, and the storage quota this node gives it.
//
// Friends are added from a contact card (see Card), exchanged out of band like
// Syncthing device IDs. The file holds no secrets.
package friends

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"hidage/internal/fsx"
)

// Friend is one entry in the friend list.
type Friend struct {
	PubKey   string   `json:"-"` // hex Ed25519 public key (the map key on disk)
	Nickname string   `json:"nickname"`
	Addrs    []string `json:"addrs,omitempty"` // multiaddrs without the /p2p/<id> suffix
	// AddrSeq and AddrSig are set when Addrs came from the friend's own signed
	// address record (so it can be passed on to other friends); a card leaves
	// them empty.
	AddrSeq  int64     `json:"addrSeq,omitempty"`
	AddrSig  []byte    `json:"addrSig,omitempty"`
	Give     int64     `json:"give"` // bytes this node will store for them
	AddedAt  time.Time `json:"addedAt"`
	LastSeen time.Time `json:"lastSeen,omitzero"`

	// What they store for us, as they last reported it on a push.
	StoredThere int64 `json:"storedThere,omitempty"` // bytes of ours they hold
	QuotaThere  int64 `json:"quotaThere,omitempty"`  // bytes they give us

	// LastAudit is the most recent proof-of-storage check of our data on them.
	LastAudit *Audit `json:"lastAudit,omitempty"`

	// PairToken is set while we hold their invite but haven't yet presented it
	// (their node was unreachable); our node retries until PairExpires.
	PairToken   []byte    `json:"pairToken,omitempty"`
	PairExpires time.Time `json:"pairExpires,omitzero"`
}

// PairingPending reports whether an unexpired invite still has to be presented.
func (f *Friend) PairingPending() bool {
	return len(f.PairToken) > 0 && time.Now().Before(f.PairExpires)
}

// Audit is the outcome of challenging a friend to prove it holds our shards.
type Audit struct {
	At      time.Time `json:"at"`
	Sampled int       `json:"sampled"` // shards challenged
	Passed  int       `json:"passed"`  // shards proven held
}

// OK reports whether every challenged shard was proven held.
func (a *Audit) OK() bool { return a != nil && a.Sampled > 0 && a.Passed == a.Sampled }

// PeerID returns the friend's libp2p peer ID (derived from the public key).
func (f *Friend) PeerID() (peer.ID, error) { return PeerIDFromHex(f.PubKey) }

// Short is a compact display form of the friend's key.
func (f *Friend) Short() string { return f.PubKey[:12] }

type fileFormat struct {
	Version int                `json:"version"`
	Friends map[string]*Friend `json:"friends"`
}

// Book is the persisted friend list. It is safe for concurrent use.
type Book struct {
	path    string
	mu      sync.Mutex
	friends map[string]*Friend
	modTime time.Time // of the file as last read or written, for Refresh
}

// Load reads the friend list at path, returning an empty list if it is absent.
func Load(path string) (*Book, error) {
	b := &Book{path: path, friends: map[string]*Friend{}}
	if err := b.readLocked(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Book) readLocked() error {
	info, err := os.Stat(b.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read friend list: %w", err)
	}
	raw, err := os.ReadFile(b.path)
	if err != nil {
		return fmt.Errorf("read friend list: %w", err)
	}
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("parse friend list: %w", err)
	}
	friends := map[string]*Friend{}
	for k, fr := range f.Friends {
		fr.PubKey = k
		friends[k] = fr
	}
	b.friends, b.modTime = friends, info.ModTime()
	return nil
}

// Refresh re-reads the file if another process (e.g. `hidage friend add` while
// the daemon runs) changed it since this Book last read or wrote it.
func (b *Book) Refresh() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	info, err := os.Stat(b.path)
	if err != nil || info.ModTime().Equal(b.modTime) {
		return nil
	}
	return b.readLocked()
}

// Exists reports whether a friend list file is present.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ImportTrust converts a legacy trust policy file (trusted keys) into friends with
// the given quota, returning how many were imported. Blocked keys are dropped:
// with a friends-only list, everyone not on it is refused anyway.
func (b *Book) ImportTrust(trustPath string, give int64) (int, error) {
	raw, err := os.ReadFile(trustPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read trust policy: %w", err)
	}
	var legacy struct {
		Trusted []string `json:"trusted"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return 0, fmt.Errorf("parse trust policy: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, k := range legacy.Trusted {
		key, err := NormalizeKey(k)
		if err != nil {
			continue
		}
		if _, ok := b.friends[key]; ok {
			continue
		}
		b.friends[key] = &Friend{PubKey: key, Nickname: b.uniqueNickLocked(key[:8], key), Give: give, AddedAt: time.Now().UTC()}
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, b.saveLocked()
}

// Add adds a friend from a contact card, or refreshes an existing friend's
// addresses (keeping its nickname and quota). nickname overrides the card's
// suggestion when set. It returns the stored entry and whether it was new.
func (b *Book) Add(c *Card, nickname string, give int64) (*Friend, bool, error) {
	key := hex.EncodeToString(c.PubKey)
	b.mu.Lock()
	defer b.mu.Unlock()
	if f, ok := b.friends[key]; ok {
		if len(c.Addrs) > 0 {
			f.Addrs, f.AddrSeq, f.AddrSig = c.AddrStrings(), 0, nil
		}
		if nickname != "" && nickname != f.Nickname {
			if err := b.checkNickLocked(nickname, key); err != nil {
				return nil, false, err
			}
			f.Nickname = nickname
		}
		return f, false, b.saveLocked()
	}
	nick := nickname
	if nick == "" {
		nick = b.uniqueNickLocked(sanitizeNick(c.Nickname, key), key)
	} else if err := b.checkNickLocked(nick, key); err != nil {
		return nil, false, err
	}
	f := &Friend{PubKey: key, Nickname: nick, Addrs: c.AddrStrings(), Give: give, AddedAt: time.Now().UTC()}
	b.friends[key] = f
	return f, true, b.saveLocked()
}

// ApplyAddrs records a friend's signed address record if it is newer than the
// one held (any signed record beats addresses from a card). The caller verifies
// the signature. It reports whether the addresses changed.
func (b *Book) ApplyAddrs(pubHex string, addrs []string, seq int64, sig []byte) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.friends[pubHex]
	if !ok || seq <= f.AddrSeq || len(addrs) == 0 {
		return false, nil
	}
	f.Addrs, f.AddrSeq, f.AddrSig = addrs, seq, sig
	return true, b.saveLocked()
}

// Remove drops a friend by public key.
func (b *Book) Remove(pubHex string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.friends[pubHex]; !ok {
		return fmt.Errorf("not a friend: %s", pubHex)
	}
	delete(b.friends, pubHex)
	return b.saveLocked()
}

// Update applies fn to the friend with the given key and saves the list.
func (b *Book) Update(pubHex string, fn func(f *Friend) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.friends[pubHex]
	if !ok {
		return fmt.Errorf("not a friend: %s", pubHex)
	}
	cp := *f
	if err := fn(&cp); err != nil {
		return err
	}
	if cp.Nickname != f.Nickname {
		if err := b.checkNickLocked(cp.Nickname, pubHex); err != nil {
			return err
		}
	}
	*f = cp
	return b.saveLocked()
}

// Get returns a copy of the friend with the given hex key.
func (b *Book) Get(pubHex string) (Friend, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.friends[pubHex]
	if !ok {
		return Friend{}, false
	}
	return *f, true
}

// IsFriend reports whether the hex public key belongs to a friend.
func (b *Book) IsFriend(pubHex string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.friends[pubHex]
	return ok
}

// IsFriendPeer reports whether a libp2p peer ID belongs to a friend.
func (b *Book) IsFriendPeer(id peer.ID) bool {
	pubHex, err := PubKeyHexFromPeerID(id)
	return err == nil && b.IsFriend(pubHex)
}

// List returns copies of all friends, sorted by nickname.
func (b *Book) List() []Friend {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Friend, 0, len(b.friends))
	for _, f := range b.friends {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Nickname < out[j].Nickname })
	return out
}

// Resolve finds a friend by nickname, hex public key (or a unique prefix of at
// least 8 characters), or libp2p peer ID.
func (b *Book) Resolve(sel string) (Friend, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, f := range b.friends {
		if f.Nickname == sel {
			return *f, nil
		}
	}
	if pid, err := peer.Decode(sel); err == nil {
		if h, err := PubKeyHexFromPeerID(pid); err == nil {
			if f, ok := b.friends[h]; ok {
				return *f, nil
			}
		}
	}
	lower := strings.ToLower(sel)
	if len(lower) >= 8 {
		var match *Friend
		for k, f := range b.friends {
			if strings.HasPrefix(k, lower) {
				if match != nil {
					return Friend{}, fmt.Errorf("ambiguous friend %q", sel)
				}
				match = f
			}
		}
		if match != nil {
			return *match, nil
		}
	}
	return Friend{}, fmt.Errorf("no friend %q (see `hidage friend ls`)", sel)
}

func (b *Book) checkNickLocked(nick, key string) error {
	if nick == "" || strings.ContainsAny(nick, " \t\n") {
		return fmt.Errorf("invalid nickname %q (no spaces)", nick)
	}
	for k, f := range b.friends {
		if k != key && f.Nickname == nick {
			return fmt.Errorf("nickname %q is already used by %s", nick, f.Short())
		}
	}
	return nil
}

// uniqueNickLocked returns nick, or nick-2, nick-3, … if it is taken.
func (b *Book) uniqueNickLocked(nick, key string) string {
	cand := nick
	for i := 2; b.checkNickLocked(cand, key) != nil; i++ {
		cand = fmt.Sprintf("%s-%d", nick, i)
	}
	return cand
}

func sanitizeNick(nick, key string) string {
	nick = strings.Join(strings.Fields(nick), "-")
	if nick == "" {
		return key[:8]
	}
	return nick
}

// saveLocked writes the list atomically (temp file + rename).
func (b *Book) saveLocked() error {
	data, err := json.MarshalIndent(fileFormat{Version: 1, Friends: b.friends}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := fsx.Rename(tmp, b.path); err != nil {
		return err
	}
	if info, err := os.Stat(b.path); err == nil {
		b.modTime = info.ModTime()
	}
	return nil
}

// NormalizeKey validates a hex Ed25519 public key and returns its lower-case form.
func NormalizeKey(hexKey string) (string, error) {
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return "", fmt.Errorf("invalid hex public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return "", fmt.Errorf("public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// PeerIDFromHex derives a libp2p peer ID from a hex Ed25519 public key.
func PeerIDFromHex(h string) (peer.ID, error) {
	raw, err := hex.DecodeString(h)
	if err != nil {
		return "", err
	}
	pub, err := crypto.UnmarshalEd25519PublicKey(raw)
	if err != nil {
		return "", err
	}
	return peer.IDFromPublicKey(pub)
}

// PubKeyHexFromPeerID returns the hex Ed25519 public key inlined in a peer ID.
func PubKeyHexFromPeerID(id peer.ID) (string, error) {
	pub, err := id.ExtractPublicKey()
	if err != nil {
		return "", err
	}
	raw, err := pub.Raw()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
