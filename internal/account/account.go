// Package account defines the account manifest: a small index, published to the
// network, of all the repositories an identity backs up. The owner's head
// pointer (keyed by public key) points at this manifest's content address, so a
// single key resolves to *all* of the owner's backups — not just the most
// recently published one. The manifest needs no signature of its own: the head
// record is signed by the owner and endorses the manifest's content address, and
// the fetcher verifies that address, so authenticity follows.
package account

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Entry is one repository's current release on the network.
type Entry struct {
	RepoID    string    `json:"repoID"`
	Name      string    `json:"name"`      // the repo's source folder name, for display/selection
	ReleaseID string    `json:"releaseID"` // content address of the pack.Release
	UpdatedAt time.Time `json:"updatedAt"`
}

// Manifest is the owner's index of backups.
type Manifest struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// New returns an empty manifest.
func New() *Manifest { return &Manifest{Version: 1} }

// Parse decodes a manifest from its JSON bytes.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse account manifest: %w", err)
	}
	return &m, nil
}

// Upsert replaces the entry with the same RepoID, or appends a new one.
func (m *Manifest) Upsert(e Entry) {
	for i := range m.Entries {
		if m.Entries[i].RepoID == e.RepoID {
			m.Entries[i] = e
			return
		}
	}
	m.Entries = append(m.Entries, e)
}

// Find resolves a backup by exact name or RepoID prefix; it errors if the
// selector is ambiguous or matches nothing.
func (m *Manifest) Find(nameOrID string) (*Entry, error) {
	var matches []*Entry
	for i := range m.Entries {
		if m.Entries[i].Name == nameOrID || strings.HasPrefix(m.Entries[i].RepoID, nameOrID) {
			matches = append(matches, &m.Entries[i])
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no backup matching %q", nameOrID)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("ambiguous selector %q (%d matches)", nameOrID, len(matches))
	}
}

// Bytes returns the canonical JSON encoding (entries sorted by RepoID) so the
// same content yields the same content address.
func (m *Manifest) Bytes() ([]byte, error) {
	sort.Slice(m.Entries, func(i, j int) bool { return m.Entries[i].RepoID < m.Entries[j].RepoID })
	return json.Marshal(m)
}
