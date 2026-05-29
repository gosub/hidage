package repo

import (
	"os"
	"path/filepath"
	"testing"

	"hidage/internal/identity"
)

const testPass = "test-passphrase"

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sampleTree(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha")
	writeFile(t, filepath.Join(dir, "sub", "b.txt"), "bravo")
	writeFile(t, filepath.Join(dir, "sub", "c.txt"), "charlie")
	return dir
}

func openRepo(t *testing.T) (*Repo, identity.Identity) {
	t.Helper()
	r, err := Init(filepath.Join(t.TempDir(), "repo"), testPass)
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return r, id
}

func TestSnapshotRestoreRoundtrip(t *testing.T) {
	r, id := openRepo(t)
	src := sampleTree(t)

	snap, _, err := r.Snapshot(src, id.Private)
	if err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	if err := r.Restore(snap, out); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"data/a.txt":     "alpha",
		"data/sub/b.txt": "bravo",
		"data/sub/c.txt": "charlie",
	} {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", rel, got, want)
		}
	}
}

func TestIncrementalSnapshotStoresOnlyDelta(t *testing.T) {
	r, id := openRepo(t)
	src := sampleTree(t)

	_, s1, err := r.Snapshot(src, id.Private)
	if err != nil {
		t.Fatal(err)
	}

	// Re-snapshot unchanged tree: should store nothing new.
	_, s2, err := r.Snapshot(src, id.Private)
	if err != nil {
		t.Fatal(err)
	}
	if s2.NewObjects != 0 {
		t.Fatalf("unchanged re-snapshot stored %d new objects, want 0", s2.NewObjects)
	}

	// Change one file: should store only that file's new chunk + the node path
	// to the root, far fewer than the first snapshot's object count.
	writeFile(t, filepath.Join(src, "a.txt"), "alpha modified")
	_, s3, err := r.Snapshot(src, id.Private)
	if err != nil {
		t.Fatal(err)
	}
	if s3.NewObjects == 0 {
		t.Fatal("changed snapshot stored nothing")
	}
	if s3.NewObjects >= s1.NewObjects {
		t.Fatalf("delta snapshot (%d) not smaller than initial (%d)", s3.NewObjects, s1.NewObjects)
	}
}

func TestHistoryAndDiff(t *testing.T) {
	r, id := openRepo(t)
	src := sampleTree(t)

	snapA, _, _ := r.Snapshot(src, id.Private)
	writeFile(t, filepath.Join(src, "a.txt"), "changed")
	writeFile(t, filepath.Join(src, "new.txt"), "newfile")
	snapB, _, _ := r.Snapshot(src, id.Private)

	if snapB.Parent != snapA.ID {
		t.Fatalf("snapB parent = %q, want %q", snapB.Parent, snapA.ID)
	}

	changes, err := r.Diff(snapA, snapB)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, c := range changes {
		paths[c.Path] = true
	}
	if !paths["data/a.txt"] || !paths["data/new.txt"] {
		t.Fatalf("diff missing expected paths: %+v", changes)
	}

	// Earlier snapshot still restores its original content.
	out := t.TempDir()
	if err := r.Restore(snapA, out); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(out, "data", "a.txt"))
	if string(got) != "alpha" {
		t.Fatalf("snapA restore: a.txt = %q, want alpha", got)
	}
}

func TestForgetAndPrune(t *testing.T) {
	r, id := openRepo(t)
	src := sampleTree(t)

	snapA, _, _ := r.Snapshot(src, id.Private)
	writeFile(t, filepath.Join(src, "a.txt"), "changed")
	snapB, _, _ := r.Snapshot(src, id.Private)

	before, _ := r.store.List()

	// Forget the older snapshot and prune: objects only it referenced go away,
	// but snapB stays fully restorable.
	if err := r.Forget(snapA.ID); err != nil {
		t.Fatal(err)
	}
	stats, err := r.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deleted == 0 {
		t.Fatal("expected prune to delete the now-unreferenced object")
	}
	after, _ := r.store.List()
	if len(after) >= len(before) {
		t.Fatalf("store did not shrink: before %d, after %d", len(before), len(after))
	}

	out := t.TempDir()
	if err := r.Restore(snapB, out); err != nil {
		t.Fatalf("snapB should still restore after prune: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(out, "data", "a.txt"))
	if string(got) != "changed" {
		t.Fatalf("snapB restore: a.txt = %q, want changed", got)
	}
}

func TestWrongPassphraseFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	if _, err := Init(dir, "right"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, "wrong"); err == nil {
		t.Fatal("expected Open with wrong passphrase to fail")
	}
}
