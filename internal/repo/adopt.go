package repo

import (
	"fmt"
	"os"

	"hidage/internal/dag"
)

// Adopt turns a folder restored from the network back into the repository it
// came from: it writes .hidage/ with the original repoID, the signed snapshot
// (byte for byte, so a re-publish rebuilds identical shards), and every DAG node
// and chunk of that snapshot, read through get. Publishing the adopted repo then
// renews the existing copies on friends instead of uploading everything again.
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
	if err := os.WriteFile(r.snapshotPath(snap.ID), snapBytes, 0o600); err != nil {
		return nil, err
	}

	src := getterFunc(get)
	l := dag.NewLoader(src, src, masterKey)
	var copyTree func(ref dag.Ref) error
	copyObj := func(ref dag.Ref, into interface{ Put([]byte) (string, error) }) error {
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
		return nil, fmt.Errorf("adopt %s: %w", folder, err)
	}
	// Creating .hidage/ bumped the folder's mtime; put back the recorded one, so
	// the next snapshot of an unchanged folder stores nothing new.
	if root, err := l.Tree(snap.Root); err == nil && len(root.Entries) == 1 {
		restoreModTime(r.Folder, root.Entries[0].ModTime)
	}
	return r, nil
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
