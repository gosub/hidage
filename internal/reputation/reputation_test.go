package reputation

import (
	"path/filepath"
	"testing"
)

const key = "aa" // any hex key works as a map key here

func TestUnknownPeerNeutral(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "rep.json"))
	if s.Score(key) != 0.5 {
		t.Fatalf("unknown score = %v, want 0.5", s.Score(key))
	}
	if s.Good(key) {
		t.Fatal("unknown peer should not be Good")
	}
	if s.Bad(key) {
		t.Fatal("unknown peer should not be Bad (no history)")
	}
}

func TestGoodAfterPasses(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "rep.json"))
	for i := 0; i < minSamples; i++ {
		s.Record(key, true)
	}
	if !s.Good(key) {
		t.Fatalf("expected Good after %d passes (score %.2f)", minSamples, s.Score(key))
	}
	if s.Bad(key) {
		t.Fatal("all-passes peer should not be Bad")
	}
}

func TestBadAfterFails(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "rep.json"))
	for i := 0; i < minSamples+2; i++ {
		s.Record(key, false)
	}
	if !s.Bad(key) {
		t.Fatalf("expected Bad after repeated fails (score %.2f)", s.Score(key))
	}
	if s.Good(key) {
		t.Fatal("failing peer should not be Good")
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
	if len(entries) != 1 || entries[0].Passes != 1 || entries[0].Fails != 1 {
		t.Fatalf("persistence lost data: %+v", entries)
	}
}
