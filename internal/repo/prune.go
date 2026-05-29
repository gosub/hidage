package repo

import (
	"fmt"
	"os"

	"hidage/internal/dag"
)

// Diff returns the paths that differ between two snapshots' trees.
func (r *Repo) Diff(a, b *Snapshot) ([]dag.Change, error) {
	l := dag.NewLoader(r.store, r.masterKey)
	return l.Diff(a.Root, b.Root)
}

// Forget deletes a snapshot's root record. The objects it referenced remain in
// the store until Prune sweeps the ones no surviving snapshot needs.
func (r *Repo) Forget(id string) error {
	snap, err := r.FindSnapshot(id)
	if err != nil {
		return err
	}
	if err := os.Remove(r.snapshotPath(snap.ID)); err != nil {
		return fmt.Errorf("remove snapshot %s: %w", snap.ID, err)
	}
	return nil
}

// PruneStats reports a garbage-collection sweep.
type PruneStats struct {
	Kept    int
	Deleted int
}

// Prune deletes every stored object not reachable from a surviving snapshot.
func (r *Repo) Prune() (*PruneStats, error) {
	snaps, err := r.ListSnapshots()
	if err != nil {
		return nil, err
	}
	l := dag.NewLoader(r.store, r.masterKey)

	reachable := make(map[string]struct{})
	for _, s := range snaps {
		set, err := l.Reachable(s.Root)
		if err != nil {
			return nil, err
		}
		for id := range set {
			reachable[id] = struct{}{}
		}
	}

	ids, err := r.store.List()
	if err != nil {
		return nil, err
	}
	stats := &PruneStats{}
	for _, id := range ids {
		if _, ok := reachable[id]; ok {
			stats.Kept++
			continue
		}
		if err := r.store.Delete(id); err != nil {
			return nil, fmt.Errorf("delete object %s: %w", id, err)
		}
		stats.Deleted++
	}
	return stats, nil
}
