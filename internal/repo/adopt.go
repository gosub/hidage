package repo

import (
	"fmt"
	"os"
	"path/filepath"

	"hidage/internal/dag"
	"hidage/internal/store"
)

// Adopt turns a folder restored from the network back into the repository it
// came from: it writes .hidage/ with the original repoID and imports the
// snapshot (see Import), then restores the folder's recorded mtime. Publishing
// the adopted repo renews the existing copies on friends instead of uploading
// everything again.
func Adopt(folder string, masterKey []byte, dataDir, repoID string, snapBytes []byte, get func(id string) ([]byte, error)) (*Repo, error) {
	snap, err := ParseSnapshot(snapBytes)
	if err != nil {
		return nil, err
	}
	if err := snap.Verify(); err != nil {
		return nil, err
	}
	r, err := initWithID(folder, masterKey, dataDir, repoID)
	if err != nil {
		return nil, err
	}
	if err := r.Import(snapBytes, get); err != nil {
		return nil, err
	}
	// Creating .hidage/ bumped the folder's mtime; put back the recorded one, so
	// the next snapshot of an unchanged folder stores nothing new.
	if root, err := r.Loader().Tree(snap.Root); err == nil && len(root.Entries) == 1 {
		restoreModTime(r.Folder, root.Entries[0].ModTime)
	}
	return r, nil
}

// Import adds a snapshot obtained elsewhere (e.g. an older version recovered
// from friends) to the repository: the signed snapshot byte for byte, so a
// re-publish rebuilds identical shards, and every DAG node and chunk it
// references, read through get.
func (r *Repo) Import(snapBytes []byte, get func(id string) ([]byte, error)) error {
	snap, err := ParseSnapshot(snapBytes)
	if err != nil {
		return err
	}
	if err := snap.Verify(); err != nil {
		return err
	}
	src := getterFunc(get)
	l := dag.NewLoader(src, src, r.masterKey)
	copyObj := func(ref dag.Ref, into store.Store) error {
		if ok, _ := into.Has(ref.CipherID); ok {
			return nil // already here (shared with another version)
		}
		data, err := get(ref.CipherID)
		if err != nil {
			return fmt.Errorf("object %s: %w", ref.CipherID, err)
		}
		id, err := into.Put(data)
		if err != nil {
			return err
		}
		if id != ref.CipherID {
			return fmt.Errorf("object %s failed its integrity check", ref.CipherID)
		}
		return nil
	}
	var copyTree func(ref dag.Ref) error
	copyTree = func(ref dag.Ref) error {
		if err := copyObj(ref, r.meta); err != nil {
			return err
		}
		t, err := l.Tree(ref)
		if err != nil {
			return err
		}
		for _, e := range t.Entries {
			switch e.Kind {
			case dag.KindTree:
				if err := copyTree(e.Ref); err != nil {
					return err
				}
			case dag.KindFile:
				if err := copyObj(e.Ref, r.meta); err != nil {
					return err
				}
				f, err := l.File(e.Ref)
				if err != nil {
					return err
				}
				for _, c := range f.Chunks {
					if err := copyObj(c, r.data); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err := copyTree(snap.Root); err != nil {
		return fmt.Errorf("import snapshot %s: %w", snap.ID[:12], err)
	}
	return os.WriteFile(r.snapshotPath(snap.ID), snapBytes, 0o600)
}

// releasePath is where the Release a snapshot was last published as is cached.
func (r *Repo) releasePath(snapID string) string {
	return filepath.Join(r.Folder, hidageDir, "releases", snapID+".json")
}

// SaveRelease caches the Release (JSON) a snapshot was published as, so later
// publishes can list its shards without rebuilding them.
func (r *Repo) SaveRelease(snapID string, relBytes []byte) error {
	p := r.releasePath(snapID)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, relBytes, 0o600)
}

// LoadRelease returns a snapshot's cached Release JSON.
func (r *Repo) LoadRelease(snapID string) ([]byte, error) {
	return os.ReadFile(r.releasePath(snapID))
}

// RootName returns the name of the folder a snapshot captured (its root tree's
// single entry), i.e. the directory RestoreWith creates under outDir.
func RootName(l *dag.Loader, snap *Snapshot) (string, error) {
	root, err := l.Tree(snap.Root)
	if err != nil {
		return "", err
	}
	if len(root.Entries) != 1 {
		return "", fmt.Errorf("snapshot root has %d entries, want 1", len(root.Entries))
	}
	return root.Entries[0].Name, nil
}

type getterFunc func(id string) ([]byte, error)

func (g getterFunc) Get(id string) ([]byte, error) { return g(id) }
