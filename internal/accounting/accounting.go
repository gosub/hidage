// Package accounting tracks, per peer, how many bytes this node stores for them
// versus how many they store for us, and enforces a contribution ratio. This is
// the basis of hidage's economic model: a node may consume remote space only by
// contributing more of its own. A free quota lets new relationships bootstrap
// before any reciprocation exists.
package accounting

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
)

const (
	// DefaultRatio is how many bytes we'll store for a peer per byte they store
	// for us (the "contribute 2–4×" model).
	DefaultRatio = 3.0
	// DefaultFreeQuota is the bytes we'll store for any peer with no reciprocation.
	DefaultFreeQuota = 1 << 30 // 1 GiB
)

type entry struct {
	StoredForThem  int64 `json:"storedForThem"`
	TheyStoreForUs int64 `json:"theyStoreForUs"`
}

// Ledger is the per-peer byte ledger, persisted to a file. Safe for concurrent
// use (the daemon accepts pushes from many peers at once).
type Ledger struct {
	mu    sync.Mutex
	path  string
	peers map[string]*entry // keyed by hex public key
}

// Load reads the ledger at path, returning an empty one if absent.
func Load(path string) (*Ledger, error) {
	l := &Ledger{path: path, peers: map[string]*entry{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &l.peers); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Ledger) get(pub string) *entry {
	e := l.peers[pub]
	if e == nil {
		e = &entry{}
		l.peers[pub] = e
	}
	return e
}

// Allow reports whether storing n more bytes for a peer stays within the policy:
// total stored for them must not exceed ratio × what they store for us, plus the
// free quota. (n may be 0 to test whether the peer is already over budget.)
func (l *Ledger) Allow(pub string, n int64, ratio float64, free int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.peers[pub]
	var sft, tsfu int64
	if e != nil {
		sft, tsfu = e.StoredForThem, e.TheyStoreForUs
	}
	return sft+n <= int64(ratio*float64(tsfu))+free
}

// AddStoredForThem records that we stored n bytes on behalf of a peer.
func (l *Ledger) AddStoredForThem(pub string, n int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.get(pub).StoredForThem += n
	return l.save()
}

// AddTheyStoreForUs records that a peer stored n bytes on our behalf.
func (l *Ledger) AddTheyStoreForUs(pub string, n int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.get(pub).TheyStoreForUs += n
	return l.save()
}

// Entry is one peer's accounting, for listing.
type Entry struct {
	PubKey         string
	StoredForThem  int64
	TheyStoreForUs int64
}

// List returns all peers' accounting, most stored-for-them first.
func (l *Ledger) List() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, 0, len(l.peers))
	for k, e := range l.peers {
		out = append(out, Entry{PubKey: k, StoredForThem: e.StoredForThem, TheyStoreForUs: e.TheyStoreForUs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StoredForThem > out[j].StoredForThem })
	return out
}

func (l *Ledger) save() error {
	data, err := json.MarshalIndent(l.peers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(l.path, data, 0o600)
}
