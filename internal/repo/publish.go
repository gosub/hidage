package repo

import (
	"fmt"
	"os"

	"hidage/internal/dag"
)

// PublishSet returns every object needed to restore a snapshot from the network,
// keyed by content address: the signed snapshot bytes (under the snapshot id),
// every reachable DAG node (meta store), and every chunk (data store). These are
// exactly the objects to replicate to peers.
func (r *Repo) PublishSet(snap *Snapshot) (map[string][]byte, error) {
	out := map[string][]byte{}

	snapBytes, err := os.ReadFile(r.snapshotPath(snap.ID))
	if err != nil {
		return nil, fmt.Errorf("read snapshot %s: %w", snap.ID, err)
	}
	out[snap.ID] = snapBytes

	l := dag.NewLoader(r.meta, r.data, r.masterKey)
	reachable, err := l.Reachable(snap.Root)
	if err != nil {
		return nil, err
	}
	for id := range reachable {
		data, err := r.getObject(id)
		if err != nil {
			return nil, fmt.Errorf("read object %s: %w", id, err)
		}
		out[id] = data
	}
	return out, nil
}

// getObject reads an encrypted object from whichever store holds it (DAG nodes
// in meta, chunks in data).
func (r *Repo) getObject(id string) ([]byte, error) {
	if ok, _ := r.meta.Has(id); ok {
		return r.meta.Get(id)
	}
	return r.data.Get(id)
}
