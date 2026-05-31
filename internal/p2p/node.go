// Package p2p is hidage's networking layer: a libp2p host whose peer identity is
// the node's Ed25519 key, a request/response protocol for fetching encrypted
// objects (chunks, DAG nodes, snapshots) by content address, Kademlia DHT
// discovery of who holds what, and a signed head pointer so a peer can find an
// owner's latest snapshot. Objects on the wire are already encrypted/signed, so
// serving them to untrusted peers reveals nothing.
package p2p

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"hidage/internal/identity"
)

// Blobs is the read side of a content-addressed store, used to serve objects.
type Blobs interface {
	Get(id string) ([]byte, error)
	Has(id string) (bool, error)
}

// Sink receives objects pushed by other peers (for storage on their behalf).
type Sink interface {
	Put(id string, data []byte) error
}

// Node is a running libp2p host.
type Node struct {
	host       host.Host
	id         identity.Identity
	blobs      Blobs
	dht        *dht.IpfsDHT
	sink       Sink
	acceptPush func(senderPub []byte) bool
	onStored   func(senderPub []byte, bytes int64)
	repLookup  func(pubHex string) (score, samples float64)
}

// New starts a libp2p host whose peer ID is derived from id (the node's Ed25519
// identity), listening on listen (a multiaddr; empty = an ephemeral TCP port on
// all interfaces). It serves objects from blobs.
func New(id identity.Identity, blobs Blobs, listen string) (*Node, error) {
	priv, err := crypto.UnmarshalEd25519PrivateKey(id.Private)
	if err != nil {
		return nil, fmt.Errorf("convert identity key: %w", err)
	}
	if listen == "" {
		listen = "/ip4/0.0.0.0/tcp/0"
	}
	h, err := libp2p.New(libp2p.Identity(priv), libp2p.ListenAddrStrings(listen))
	if err != nil {
		return nil, fmt.Errorf("start libp2p host: %w", err)
	}
	n := &Node{host: h, id: id, blobs: blobs}
	n.registerObjectHandler()
	n.registerAuditHandler()
	return n, nil
}

// ID returns the node's libp2p peer ID (derived from its Ed25519 public key).
func (n *Node) ID() peer.ID { return n.host.ID() }

// Addrs returns the node's full dialable addresses (each ending in /p2p/<id>).
func (n *Node) Addrs() []string {
	var out []string
	info := peer.AddrInfo{ID: n.host.ID(), Addrs: n.host.Addrs()}
	addrs, err := peer.AddrInfoToP2pAddrs(&info)
	if err != nil {
		return out
	}
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}

// ConnectInfo connects to a peer given its discovered address info.
func (n *Node) ConnectInfo(ctx context.Context, ai peer.AddrInfo) error {
	return n.host.Connect(ctx, ai)
}

// Connect dials a peer given a full multiaddr (…/p2p/<id>).
func (n *Node) Connect(ctx context.Context, addr string) (peer.ID, error) {
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return "", fmt.Errorf("parse multiaddr: %w", err)
	}
	info, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		return "", fmt.Errorf("parse peer addr: %w", err)
	}
	if err := n.host.Connect(ctx, *info); err != nil {
		return "", fmt.Errorf("connect %s: %w", info.ID, err)
	}
	return info.ID, nil
}

// Close shuts the host down.
func (n *Node) Close() error { return n.host.Close() }

// PeerIDFromPubKeyHex derives a libp2p peer ID from a hex Ed25519 public key
// (the format the trust store uses), so a trusted key can be located on the DHT.
func PeerIDFromPubKeyHex(h string) (peer.ID, error) {
	raw, err := hex.DecodeString(h)
	if err != nil {
		return "", err
	}
	pub, err := crypto.UnmarshalEd25519PublicKey(raw)
	if err != nil {
		return "", err
	}
	return peer.IDFromPublicKey(pub)
}

// PeerPubKeyHex returns the hex Ed25519 public key for a peer ID (the key is
// inlined in the ID), matching the format used by the trust and reputation stores.
func PeerPubKeyHex(id peer.ID) (string, error) {
	pub, err := id.ExtractPublicKey()
	if err != nil {
		return "", err
	}
	raw, err := pub.Raw()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
