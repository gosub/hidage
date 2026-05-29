package repo

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"hidage/internal/crypto"
	"hidage/internal/identity"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo creates a tracked folder with a sample tree and an initialized repo,
// returning the repo, a signing key, and the folder path.
func newRepo(t *testing.T) (*Repo, ed25519.PrivateKey, string) {
	t.Helper()
	folder := filepath.Join(t.TempDir(), "data")
	writeFile(t, filepath.Join(folder, "a.txt"), "alpha")
	writeFile(t, filepath.Join(folder, "sub", "b.txt"), "bravo")
	writeFile(t, filepath.Join(folder, "sub", "c.txt"), "charlie")

	mk, _ := crypto.NewDEK()
	r, err := Init(folder, mk, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return r, id.Private, folder
}

func TestSnapshotRestoreRoundtrip(t *testing.T) {
	r, priv, _ := newRepo(t)
	snap, _, err := r.Snapshot(priv, false)
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

func TestSkipsHidageDir(t *testing.T) {
	r, priv, folder := newRepo(t)
	snap, _, err := r.Snapshot(priv, false)
	if err != nil {
		t.Fatal(err)
	}
	// .hidage exists in the tracked folder but must not appear in a restore.
	if _, err := os.Stat(filepath.Join(folder, ".hidage")); err != nil {
		t.Fatal("expected .hidage to exist in the tracked folder")
	}
	out := t.TempDir()
	if err := r.Restore(snap, out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "data", ".hidage")); !os.IsNotExist(err) {
		t.Fatal(".hidage should never be backed up into a snapshot")
	}
}

func TestIncrementalSnapshotStoresOnlyDelta(t *testing.T) {
	r, priv, folder := newRepo(t)

	_, s1, err := r.Snapshot(priv, false)
	if err != nil {
		t.Fatal(err)
	}
	// Unchanged re-snapshot stores nothing.
	_, s2, err := r.Snapshot(priv, false)
	if err != nil {
		t.Fatal(err)
	}
	if s2.NewObjects != 0 {
		t.Fatalf("unchanged re-snapshot stored %d new objects, want 0", s2.NewObjects)
	}
	// One small change stores far fewer objects than the initial snapshot.
	writeFile(t, filepath.Join(folder, "a.txt"), "alpha modified")
	_, s3, err := r.Snapshot(priv, false)
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
	r, priv, folder := newRepo(t)
	snapA, _, _ := r.Snapshot(priv, false)
	writeFile(t, filepath.Join(folder, "a.txt"), "changed")
	writeFile(t, filepath.Join(folder, "new.txt"), "newfile")
	snapB, _, _ := r.Snapshot(priv, false)

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

func TestReplaceSupersedesPrevious(t *testing.T) {
	r, priv, folder := newRepo(t)
	snapA, _, _ := r.Snapshot(priv, false)

	writeFile(t, filepath.Join(folder, "a.txt"), "changed")
	snapB, stats, err := r.Snapshot(priv, true) // --replace
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pruned == 0 {
		t.Fatal("replace should prune objects only the previous snapshot held")
	}
	snaps, err := r.ListSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].ID != snapB.ID {
		t.Fatalf("after replace want only snapB, got %d snapshots", len(snaps))
	}
	if snapB.Parent != snapA.Parent {
		t.Fatalf("replacement should chain past the dropped snapshot")
	}
	// The replacement still restores.
	out := t.TempDir()
	if err := r.Restore(snapB, out); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(out, "data", "a.txt"))
	if string(got) != "changed" {
		t.Fatalf("snapB restore: a.txt = %q, want changed", got)
	}
}

func TestForgetAndPrune(t *testing.T) {
	r, priv, folder := newRepo(t)
	snapA, _, _ := r.Snapshot(priv, false)
	writeFile(t, filepath.Join(folder, "a.txt"), "changed")
	snapB, _, _ := r.Snapshot(priv, false)

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
	out := t.TempDir()
	if err := r.Restore(snapB, out); err != nil {
		t.Fatalf("snapB should still restore after prune: %v", err)
	}
}
