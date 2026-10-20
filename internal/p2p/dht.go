package p2p

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/ipfs/go-cid"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
)

// WaitForPeers blocks until the DHT routing table holds at least min peers, or
// the timeout/ctx elapses. Record puts/gets are unreliable until the table is
// populated, so callers wait after bootstrapping before publishing or resolving
// head pointers.
func (n *Node) WaitForPeers(ctx context.Context, min int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if n.dht != nil && n.dht.RoutingTable().Size() >= min {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// EnableDHT attaches a Kademlia DHT (server mode) to the host, used to discover
// which peers hold a given object and to publish/resolve signed head pointers.
func (n *Node) EnableDHT(ctx context.Context) error {
	d, err := dht.New(ctx, n.host,
		dht.Mode(dht.ModeServer),
		dht.ProtocolPrefix("/hidage"),
		dht.NamespacedValidator(headNamespace, headValidator{}),
	)
	if err != nil {
		return fmt.Errorf("start DHT: %w", err)
	}
	n.dht = d
	return nil
}

// Bootstrap seeds the routing table and connects to the provided bootstrap
// peers (full multiaddrs ending in /p2p/<id>). Unreachable peers are skipped; it
// fails only if peers were given and none could be reached.
func (n *Node) Bootstrap(ctx context.Context, peers []string) error {
	var firstErr error
	ok := 0
	for _, addr := range peers {
		if _, err := n.Connect(ctx, addr); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ok++
	}
	if len(peers) > 0 && ok == 0 {
		return firstErr
	}
	return n.dht.Bootstrap(ctx)
}

// idToCID maps a hex SHA-256 object id to a raw CIDv1 for DHT routing.
func idToCID(id string) (cid.Cid, error) {
	raw, err := hex.DecodeString(id)
	if err != nil {
		return cid.Undef, fmt.Errorf("invalid object id: %w", err)
	}
	mh, err := multihash.Encode(raw, multihash.SHA2_256)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, mh), nil
}

// Provide announces to the DHT that this node holds the given object.
func (n *Node) Provide(ctx context.Context, id string) error {
	c, err := idToCID(id)
	if err != nil {
		return err
	}
	return n.dht.Provide(ctx, c, true)
}

// DialPeerID locates a peer by ID via the DHT and connects to it. Used to reach
// a trusted peer known only by its public key (no multiaddr supplied).
func (n *Node) DialPeerID(ctx context.Context, pid peer.ID) error {
	ai, err := n.dht.FindPeer(ctx, pid)
	if err != nil {
		return fmt.Errorf("locate %s: %w", pid, err)
	}
	return n.host.Connect(ctx, ai)
}

// FindProviders returns peers that have announced the given object.
func (n *Node) FindProviders(ctx context.Context, id string) ([]peer.AddrInfo, error) {
	c, err := idToCID(id)
	if err != nil {
		return nil, err
	}
	return n.dht.FindProviders(ctx, c)
}
