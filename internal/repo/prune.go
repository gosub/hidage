package repo

import (
	"fmt"

	"hidage/internal/dag"
	"hidage/internal/fsx"
	"hidage/internal/store"
)

// Diff returns the paths that differ between two snapshots' trees.
func (r *Repo) Diff(a, b *Snapshot) ([]dag.Change, error) {
	l := dag.NewLoader(r.meta, r.data, r.masterKey)
	return l.Diff(a.Root, b.Root)
}

// Forget deletes a snapshot's root record. The objects it referenced remain in
// the stores until Prune sweeps the ones no surviving snapshot needs.
func (r *Repo) Forget(id string) error {
	snap, err := r.FindSnapshot(id)
	if err != nil {
		return err
	}
	if err := fsx.Remove(r.snapshotPath(snap.ID)); err != nil {
		return fmt.Errorf("remove snapshot %s: %w", snap.ID, err)
	}
	fsx.Remove(r.releasePath(snap.ID)) // its cached release, if published
	return nil
}

// PruneStats reports a garbage-collection sweep.
type PruneStats struct {
	Kept    int
	Deleted int
}

// Prune deletes every stored object (in both the meta and data stores) not
// reachable from a surviving snapshot.
func (r *Repo) Prune() (*PruneStats, error) {
	snaps, err := r.ListSnapshots()
	if err != nil {
		return nil, err
	}
	l := dag.NewLoader(r.meta, r.data, r.masterKey)

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

	stats := &PruneStats{}
	for _, st := range []store.Store{r.meta, r.data} {
		if err := sweep(st, reachable, stats); err != nil {
			return nil, err
		}
	}
	return stats, nil
}

func sweep(st store.Store, reachable map[string]struct{}, stats *PruneStats) error {
	ids, err := st.List()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, ok := reachable[id]; ok {
			stats.Kept++
			continue
		}
		if err := st.Delete(id); err != nil {
			return fmt.Errorf("delete object %s: %w", id, err)
		}
		stats.Deleted++
	}
	return nil
}
