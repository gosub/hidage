package account

import (
	"testing"
	"time"
)

func TestUpsertReplacesByRepoID(t *testing.T) {
	m := New()
	m.Upsert(Entry{RepoID: "a", Name: "project", ReleaseID: "r1", UpdatedAt: time.Now()})
	m.Upsert(Entry{RepoID: "b", Name: "photos", ReleaseID: "r2", UpdatedAt: time.Now()})
	if len(m.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(m.Entries))
	}
	// Re-publishing repo "a" replaces its entry, not appends.
	m.Upsert(Entry{RepoID: "a", Name: "project", ReleaseID: "r3", UpdatedAt: time.Now()})
	if len(m.Entries) != 2 {
		t.Fatalf("upsert should replace, got %d entries", len(m.Entries))
	}
	e, err := m.Find("project")
	if err != nil || e.ReleaseID != "r3" {
		t.Fatalf("find project: %v / %+v", err, e)
	}
}

func TestFindByNameAndIDPrefix(t *testing.T) {
	m := New()
	m.Upsert(Entry{RepoID: "deadbeef01", Name: "project"})
	m.Upsert(Entry{RepoID: "feedface02", Name: "photos"})

	if e, err := m.Find("photos"); err != nil || e.RepoID != "feedface02" {
		t.Fatalf("find by name: %v", err)
	}
	if e, err := m.Find("dead"); err != nil || e.Name != "project" {
		t.Fatalf("find by id prefix: %v", err)
	}
	if _, err := m.Find("nope"); err == nil {
		t.Fatal("expected miss to error")
	}
}

func TestBytesRoundtripDeterministic(t *testing.T) {
	m := New()
	m.Upsert(Entry{RepoID: "b", Name: "two", ReleaseID: "r2"})
	m.Upsert(Entry{RepoID: "a", Name: "one", ReleaseID: "r1"})
	b1, _ := m.Bytes()
	b2, _ := m.Bytes()
	if string(b1) != string(b2) {
		t.Fatal("Bytes() should be deterministic")
	}
	parsed, err := Parse(b1)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Entries) != 2 || parsed.Entries[0].RepoID != "a" {
		t.Fatalf("roundtrip/sort wrong: %+v", parsed.Entries)
	}
}
