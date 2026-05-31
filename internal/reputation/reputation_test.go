package reputation

import (
	"path/filepath"
	"testing"
	"time"
)

const key = "aa" // any hex key works as a map key here

func TestUnknownPeerNeutral(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "rep.json"))
	if s.Score(key) != 0.5 {
		t.Fatalf("unknown score = %v, want 0.5", s.Score(key))
	}
	if s.Good(key) || s.Bad(key) {
		t.Fatal("unknown peer should be neither Good nor Bad")
	}
}

func TestGoodAfterPasses(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "rep.json"))
	for i := 0; i < 3; i++ {
		s.Record(key, true)
	}
	if !s.Good(key) {
		t.Fatalf("expected Good after 3 passes (score %.2f)", s.Score(key))
	}
	if s.Bad(key) {
		t.Fatal("all-passes peer should not be Bad")
	}
}

func TestBadAfterFails(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "rep.json"))
	for i := 0; i < 5; i++ {
		s.Record(key, false)
	}
	if !s.Bad(key) {
		t.Fatalf("expected Bad after repeated fails (score %.2f)", s.Score(key))
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rep.json")
	s, _ := Load(path)
	s.Record(key, true)
	s.Record(key, false)

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	entries := reloaded.List()
	// Recorded microseconds apart, so decay is negligible: ~1 pass, ~1 fail.
	if len(entries) != 1 || entries[0].Passes < 0.99 || entries[0].Fails < 0.99 {
		t.Fatalf("persistence lost data: %+v", entries)
	}
}

func TestDecayRevertsToUnproven(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "rep.json"))
	for i := 0; i < 5; i++ {
		s.Record(key, true)
	}
	if !s.Good(key) {
		t.Fatal("should be Good right after 5 passes")
	}
	// Four half-lives later, the 5 passes decay to ~0.3 — below minSamples.
	s.peers[key].LastSeen = time.Now().Add(-4 * decayHalfLife)
	if s.Good(key) {
		t.Fatal("stale reputation should fade back to unproven (not Good)")
	}
	if s.Bad(key) {
		t.Fatal("faded reputation has too little evidence to be Bad either")
	}
}

func TestRecentFailDominatesBankedPasses(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "rep.json"))
	for i := 0; i < 10; i++ {
		s.Record(key, true)
	}
	// Backdate the banked passes by two half-lives (weight ×0.25 → ~2.5), then a
	// fresh failure. Cumulative would be 10/1 ≈ 0.85 (Good); recency makes it Bad.
	s.peers[key].LastSeen = time.Now().Add(-2 * decayHalfLife)
	s.Record(key, false)
	if !s.Bad(key) {
		t.Fatalf("a recent failure should dominate banked passes (score %.2f)", s.Score(key))
	}
}

func TestEvidence(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "rep.json"))
	if sc, sm := s.Evidence(key); sc != 0.5 || sm != 0 {
		t.Fatalf("unknown Evidence = %v,%v, want 0.5,0", sc, sm)
	}
	for i := 0; i < 3; i++ {
		s.Record(key, true)
	}
	sc, sm := s.Evidence(key)
	if sm < MinSamples || sc < goodThreshold {
		t.Fatalf("after 3 passes Evidence = %v,%v (want samples≥%v, score≥%v)", sc, sm, MinSamples, goodThreshold)
	}
}
