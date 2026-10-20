package account

import (
	"bytes"
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

func TestRosterSealed(t *testing.T) {
	m := New()
	if r, err := m.OpenRoster(make([]byte, 32)); r != nil || err != nil {
		t.Fatalf("empty roster = %v, %v", r, err)
	}
	key := bytes.Repeat([]byte{1}, 32)
	want := []RosterEntry{{PubKey: "ab", Nickname: "bob", Addrs: []string{"/ip4/192.0.2.1/tcp/4001"}, Give: 9}}
	if err := m.SetRoster(key, want); err != nil {
		t.Fatal(err)
	}
	raw, _ := m.Bytes()
	if bytes.Contains(raw, []byte("bob")) {
		t.Fatal("roster is readable in the manifest")
	}
	parsed, _ := Parse(raw)
	got, err := parsed.OpenRoster(key)
	if err != nil || len(got) != 1 || got[0].Nickname != "bob" || got[0].Give != 9 {
		t.Fatalf("roster = %+v, %v", got, err)
	}
	if _, err := parsed.OpenRoster(bytes.Repeat([]byte{2}, 32)); err == nil {
		t.Fatal("roster opened with the wrong key")
	}
}
