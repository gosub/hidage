package hosting

import (
	"crypto/sha256"
	"encoding/json"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
)

var owner = strings.Repeat("ab", 32)

func obj(s string) (string, []byte) {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:]), []byte(s)
}

// publish replaces set with the given contents, sending what is missing.
func publish(t *testing.T, h *Host, set string, contents ...string) (missing int, held int64) {
	t.Helper()
	s, err := h.Begin(owner, set)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var entries []Entry
	var ids []string
	data := map[string][]byte{}
	for _, c := range contents {
		id, d := obj(c)
		entries = append(entries, Entry{ID: id, Size: int64(len(d))})
		ids = append(ids, id)
		data[id] = d
	}
	miss, _, err := s.Plan(entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range miss {
		if err := s.Put(entries[i].ID, data[entries[i].ID]); err != nil {
			t.Fatal(err)
		}
	}
	held, err = s.Commit(ids)
	if err != nil {
		t.Fatal(err)
	}
	return len(miss), held
}

func TestSetReplacementAndTrash(t *testing.T) {
	h, _ := Open(t.TempDir())
	set := strings.Repeat("01", 16)

	if miss, held := publish(t, h, set, "aaaa", "bbbb"); miss != 2 || held != 8 {
		t.Fatalf("first publish: missing %d held %d", miss, held)
	}
	// Re-publishing the same content sends nothing.
	if miss, _ := publish(t, h, set, "aaaa", "bbbb"); miss != 0 {
		t.Fatalf("unchanged publish sent %d objects", miss)
	}
	// Replacing bbbb with cccc trashes bbbb but keeps it readable.
	if miss, held := publish(t, h, set, "aaaa", "cccc"); miss != 1 || held != 8 {
		t.Fatalf("replace: missing %d held %d", miss, held)
	}
	bID, _ := obj("bbbb")
	if got, err := h.Get(owner, bID); err != nil || string(got) != "bbbb" {
		t.Fatalf("trashed object should still be readable: %q, %v", got, err)
	}
	live, trash, sets := h.Usage(owner)
	if live != 8 || trash != 4 || sets != 1 {
		t.Fatalf("usage = %d live, %d trash, %d sets", live, trash, sets)
	}
	// Listing it again brings it back from trash without resending it.
	if miss, _ := publish(t, h, set, "aaaa", "bbbb"); miss != 0 {
		t.Fatalf("object in trash was re-sent (%d)", miss)
	}
	if _, trash, _ := h.Usage(owner); trash != 4 { // cccc is now in trash
		t.Fatalf("trash = %d after swap back", trash)
	}
}

func TestObjectsSharedBetweenSets(t *testing.T) {
	h, _ := Open(t.TempDir())
	s1, s2 := strings.Repeat("01", 16), strings.Repeat("02", 16)
	publish(t, h, s1, "shared", "only-1")
	publish(t, h, s2, "shared", "only-2")
	// Dropping "shared" from set 1 keeps it live: set 2 still lists it.
	publish(t, h, s1, "only-1")
	id, _ := obj("shared")
	if _, err := os.Stat(h.objPath(owner, "objects", id)); err != nil {
		t.Fatalf("shared object was trashed while another set lists it: %v", err)
	}
}

func TestPlanCountsHeldAfter(t *testing.T) {
	h, _ := Open(t.TempDir())
	set := strings.Repeat("01", 16)
	publish(t, h, set, "aaaa", "bbbb")
	s, _ := h.Begin(owner, set)
	defer s.Close()
	big := Entry{ID: strings.Repeat("cd", 32), Size: 1000}
	aID, _ := obj("aaaa")
	miss, heldAfter, err := s.Plan([]Entry{{ID: aID, Size: 4}, big})
	if err != nil || len(miss) != 1 || miss[0] != 1 || heldAfter != 1004 {
		t.Fatalf("plan = %v, %d, %v; want [1], 1004 (bbbb would be dropped)", miss, heldAfter, err)
	}
}

func TestReapLeaseAndTrash(t *testing.T) {
	h, _ := Open(t.TempDir())
	set := strings.Repeat("01", 16)
	publish(t, h, set, "aaaa")
	// Age the set past the lease.
	p := h.dir(owner, "sets", set+".json")
	sf, _ := readSet(p)
	sf.UpdatedAt = time.Now().Add(-48 * time.Hour)
	data, _ := jsonMarshal(sf)
	os.WriteFile(p, data, 0o600)

	st, err := h.Reap(24*time.Hour, time.Hour)
	if err != nil || st.ExpiredSets != 1 || st.Trashed != 1 || st.DeletedTrash != 0 {
		t.Fatalf("reap 1 = %+v, %v", st, err)
	}
	// Age the trash past the grace period.
	id, _ := obj("aaaa")
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(h.objPath(owner, "trash", id), old, old)
	if st, _ := h.Reap(24*time.Hour, time.Hour); st.DeletedTrash != 1 || st.FreedBytes != 4 {
		t.Fatalf("reap 2 = %+v", st)
	}
	if _, err := h.Get(owner, id); err != ErrNotFound {
		t.Fatalf("object should be gone: %v", err)
	}
}

func TestPurgeAndValidation(t *testing.T) {
	h, _ := Open(t.TempDir())
	publish(t, h, strings.Repeat("01", 16), "aaaa")
	if err := h.Purge(owner); err != nil {
		t.Fatal(err)
	}
	if live, trash, sets := h.Usage(owner); live+trash != 0 || sets != 0 {
		t.Fatal("purge left data behind")
	}
	if _, err := h.Begin("../etc", strings.Repeat("01", 16)); err == nil {
		t.Fatal("path-like owner accepted")
	}
	if _, err := h.Get(owner, "../../x"); err != ErrNotFound {
		t.Fatal("path-like id accepted")
	}
	if len(h.Owners()) != 0 {
		t.Fatalf("owners after purge = %v", h.Owners())
	}
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
