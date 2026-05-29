package p2p

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/ipfs/go-cid"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
)

// EnableDHT attaches a Kademlia DHT (server mode) to the host, used to discover
// which peers hold a given object and to publish/resolve signed head pointers.
func (n *Node) EnableDHT(ctx context.Context) error {
	d, err := dht.New(ctx, n.host, dht.Mode(dht.ModeServer), dht.ProtocolPrefix("/hidage"))
	if err != nil {
		return fmt.Errorf("start DHT: %w", err)
	}
	n.dht = d
	return nil
}

// Bootstrap seeds the routing table and connects to any provided bootstrap
// peers (full multiaddrs ending in /p2p/<id>).
func (n *Node) Bootstrap(ctx context.Context, peers []string) error {
	for _, addr := range peers {
		if _, err := n.Connect(ctx, addr); err != nil {
			return err
		}
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

// FindProviders returns peers that have announced the given object.
func (n *Node) FindProviders(ctx context.Context, id string) ([]peer.AddrInfo, error) {
	c, err := idToCID(id)
	if err != nil {
		return nil, err
	}
	return n.dht.FindProviders(ctx, c)
}
