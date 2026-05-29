// Package reputation tracks how reliably each peer has answered proof-of-storage
// audits, so a node can prefer dependable hosts. Scores are local observations
// (Laplace-smoothed pass rate) persisted per peer public key. The trust policy's
// "neutral" verdict defers here: a neutral peer is used only once it has enough
// audit history and a high enough score.
package reputation

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"time"
)

const (
	// minSamples is how many audits a neutral peer needs before it counts as good.
	minSamples = 3
	// goodThreshold is the minimum score for a neutral peer to be used.
	goodThreshold = 0.8
)

type stat struct {
	Passes   int       `json:"passes"`
	Fails    int       `json:"fails"`
	LastSeen time.Time `json:"lastSeen"`
}

// Store is the per-peer audit history, persisted to a file.
type Store struct {
	path  string
	peers map[string]*stat // keyed by hex public key
}

// Load reads the store at path, returning an empty store if absent.
func Load(path string) (*Store, error) {
	s := &Store{path: path, peers: map[string]*stat{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.peers); err != nil {
		return nil, err
	}
	return s, nil
}

// Record notes the outcome of an audit against the given peer and persists it.
func (s *Store) Record(pubHex string, ok bool) error {
	st := s.peers[pubHex]
	if st == nil {
		st = &stat{}
		s.peers[pubHex] = st
	}
	if ok {
		st.Passes++
	} else {
		st.Fails++
	}
	st.LastSeen = time.Now().UTC()
	return s.save()
}

// Score returns the Laplace-smoothed pass rate for a peer (0.5 with no history).
func (s *Store) Score(pubHex string) float64 {
	st := s.peers[pubHex]
	if st == nil {
		return 0.5
	}
	return float64(st.Passes+1) / float64(st.Passes+st.Fails+2)
}

// Good reports whether a peer has enough history and a high enough score to rely
// on when no explicit trust decision applies.
func (s *Store) Good(pubHex string) bool {
	st := s.peers[pubHex]
	if st == nil || st.Passes+st.Fails < minSamples {
		return false
	}
	return s.Score(pubHex) >= goodThreshold
}

// Bad reports whether a peer has enough audit history and a score low enough to
// avoid. (Distinct from "not Good", which also covers peers with no history.)
func (s *Store) Bad(pubHex string) bool {
	st := s.peers[pubHex]
	if st == nil || st.Passes+st.Fails < minSamples {
		return false
	}
	return s.Score(pubHex) < goodThreshold
}

// Entry is one peer's reputation, for listing.
type Entry struct {
	PubKey string
	Passes int
	Fails  int
	Score  float64
	Good   bool
}

// List returns all peers' reputations, best score first.
func (s *Store) List() []Entry {
	out := make([]Entry, 0, len(s.peers))
	for k, st := range s.peers {
		out = append(out, Entry{PubKey: k, Passes: st.Passes, Fails: st.Fails, Score: s.Score(k), Good: s.Good(k)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

func (s *Store) save() error {
	data, err := json.MarshalIndent(s.peers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
