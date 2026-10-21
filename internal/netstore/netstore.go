// Package netstore reads objects from the P2P network instead of local disk. It
// satisfies dag.Getter, so the ordinary restore logic can rebuild a tree by
// pulling each object (snapshot, DAG node, chunk) from peers that hold it, with
// a local cache to avoid refetching. This is what makes a snapshot restorable on
// a machine that has only the recovery code and a way to reach its friends.
package netstore

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"hidage/internal/p2p"
	"hidage/internal/store"
)

// Store fetches objects from a set of peers via a libp2p node, caching them in a
// keyed store.
type Store struct {
	node    *p2p.Node
	peers   []peer.ID
	cache   *store.KeyedStore
	timeout time.Duration
}

// New returns a network-backed object source that asks peers (in order) through
// node, caching into cache.
func New(node *p2p.Node, cache *store.KeyedStore, peers []peer.ID) *Store {
	return &Store{node: node, peers: peers, cache: cache, timeout: 60 * time.Second}
}

// Get returns the object with the given content address, from cache or network.
func (s *Store) Get(id string) ([]byte, error) {
	if data, err := s.cache.Get(id); err == nil {
		return data, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	data, err := s.node.GetObject(ctx, id, s.peers)
	if err != nil {
		return nil, err
	}
	_ = s.cache.Put(id, data)
	return data, nil
}
