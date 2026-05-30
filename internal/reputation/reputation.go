// Package reputation tracks how reliably each peer has answered proof-of-storage
// audits, so a node can prefer dependable hosts. Scores are local observations
// (a recency-weighted, Laplace-smoothed pass rate) persisted per peer public key.
// They gate publishing only by *excluding proven-bad peers*: the trust policy's
// "neutral" verdict is used unless the peer has enough recent audit history and a
// low score. Peers with little (or stale) history are treated as unproven and are
// still used — reputation is earned by storing reliably, not a prerequisite.
//
// Evidence decays with wall-clock time (exponential, by decayHalfLife), so recent
// audits dominate: a peer can't bank old passes to mask recent failures, a peer
// that improves recovers, and a peer not audited in a long time fades back toward
// unproven (prompting a fresh audit).
package reputation

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"sort"
	"time"
)

const (
	// minSamples is the recent-evidence mass (decayed audit count) a peer needs
	// before it can be classified Good or Bad — roughly three recent audits, set
	// just under 3 because decay leaves the effective count slightly below the raw
	// audit count.
	minSamples = 2.5
	// goodThreshold is the score cutoff separating Good from Bad. It sits below
	// the achievable maximum (decay+smoothing cap a perfect peer near ~0.88) so a
	// steadily-passing peer is Good after ~3 audits, while a recent failure (which
	// pulls a peer toward ~0.64) drops it to Bad.
	goodThreshold = 0.7
	// decayHalfLife is how long until an audit's weight halves. Evidence older
	// than a few half-lives barely counts, so reputation reflects recent behavior.
	decayHalfLife = 30 * 24 * time.Hour
)

type stat struct {
	Passes   float64   `json:"passes"` // decayed pseudo-count, as of LastSeen
	Fails    float64   `json:"fails"`
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

// effective returns a peer's pass/fail pseudo-counts decayed forward to now.
func effective(st *stat, now time.Time) (passes, fails float64) {
	if st == nil {
		return 0, 0
	}
	elapsed := now.Sub(st.LastSeen)
	if elapsed < 0 {
		elapsed = 0
	}
	factor := math.Exp2(-float64(elapsed) / float64(decayHalfLife))
	return st.Passes * factor, st.Fails * factor
}

// Record notes the outcome of an audit against the given peer and persists it.
// Existing evidence is decayed to the present before the new observation is added,
// so the stored counts always represent the recency-weighted history.
func (s *Store) Record(pubHex string, ok bool) error {
	now := time.Now().UTC()
	st := s.peers[pubHex]
	if st == nil {
		st = &stat{}
		s.peers[pubHex] = st
	}
	p, f := effective(st, now)
	if ok {
		p++
	} else {
		f++
	}
	st.Passes, st.Fails, st.LastSeen = p, f, now
	return s.save()
}

// Score returns the recency-weighted, Laplace-smoothed pass rate (0.5 with no
// history).
func (s *Store) Score(pubHex string) float64 {
	st := s.peers[pubHex]
	if st == nil {
		return 0.5
	}
	p, f := effective(st, time.Now().UTC())
	return (p + 1) / (p + f + 2)
}

// Good reports whether a peer has enough recent history and a high enough score
// to rely on when no explicit trust decision applies.
func (s *Store) Good(pubHex string) bool {
	p, f := effective(s.peers[pubHex], time.Now().UTC())
	if p+f < minSamples {
		return false
	}
	return (p+1)/(p+f+2) >= goodThreshold
}

// Bad reports whether a peer has enough recent history and a score low enough to
// avoid. (Distinct from "not Good", which also covers peers with little history.)
func (s *Store) Bad(pubHex string) bool {
	p, f := effective(s.peers[pubHex], time.Now().UTC())
	if p+f < minSamples {
		return false
	}
	return (p+1)/(p+f+2) < goodThreshold
}

// Entry is one peer's reputation (decayed to now), for listing.
type Entry struct {
	PubKey string
	Passes float64
	Fails  float64
	Score  float64
	Good   bool
}

// List returns all peers' reputations, best score first.
func (s *Store) List() []Entry {
	now := time.Now().UTC()
	out := make([]Entry, 0, len(s.peers))
	for k := range s.peers {
		p, f := effective(s.peers[k], now)
		out = append(out, Entry{PubKey: k, Passes: p, Fails: f, Score: s.Score(k), Good: s.Good(k)})
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
