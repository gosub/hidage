package repo

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hidage/internal/crypto"
	"hidage/internal/dag"
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

func TestModTimePreservedOnRestore(t *testing.T) {
	r, priv, folder := newRepo(t)
	want := time.Unix(1_600_000_000, 0)
	if err := os.Chtimes(filepath.Join(folder, "a.txt"), want, want); err != nil {
		t.Fatal(err)
	}
	snap, _, err := r.Snapshot(priv, false)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := r.Restore(snap, out); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(out, "data", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(want) {
		t.Fatalf("restored mtime = %v, want %v", fi.ModTime(), want)
	}
}

func TestUnsupportedEntriesReported(t *testing.T) {
	r, priv, folder := newRepo(t)
	if err := os.Symlink("a.txt", filepath.Join(folder, "link")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	_, stats, err := r.Snapshot(priv, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Skipped) != 1 || filepath.Base(stats.Skipped[0]) != "link" {
		t.Fatalf("expected the symlink to be reported skipped, got %v", stats.Skipped)
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

func TestAdoptRebuildsRepository(t *testing.T) {
	r, priv, _ := newRepo(t)
	snap, _, err := r.Snapshot(priv, false)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := r.PublishSet(snap)
	if err != nil {
		t.Fatal(err)
	}
	get := func(id string) ([]byte, error) {
		if d, ok := objs[id]; ok {
			return d, nil
		}
		return nil, os.ErrNotExist
	}

	// On a "new machine": restore the files, then adopt the restored folder.
	out := t.TempDir()
	l := dag.NewLoader(getterFunc(get), getterFunc(get), r.masterKey)
	if err := RestoreWith(l, snap, out); err != nil {
		t.Fatal(err)
	}
	name, err := RootName(l, snap)
	if err != nil || name != "data" {
		t.Fatalf("RootName = %q, %v", name, err)
	}
	adopted, err := Adopt(filepath.Join(out, name), r.masterKey, t.TempDir(), r.RepoID(), objs[snap.ID], get)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.RepoID() != r.RepoID() {
		t.Fatalf("repo id %s, want %s", adopted.RepoID(), r.RepoID())
	}
	latest, err := adopted.Latest()
	if err != nil || latest == nil || latest.ID != snap.ID {
		t.Fatalf("adopted latest = %+v, %v", latest, err)
	}
	// The adopted repo yields the identical publish set (so re-publishing renews).
	again, err := adopted.PublishSet(latest)
	if err != nil || len(again) != len(objs) {
		t.Fatalf("publish set: %d objects, %v; want %d", len(again), err, len(objs))
	}
	for id, d := range objs {
		if string(again[id]) != string(d) {
			t.Fatalf("object %s differs after adopt", id)
		}
	}
	// A new snapshot of the unchanged folder stores nothing new.
	if _, stats, err := adopted.Snapshot(priv, false); err != nil || stats.NewObjects != 0 {
		t.Fatalf("snapshot after adopt stored %d new objects (%v), want 0", stats.NewObjects, err)
	}
}
