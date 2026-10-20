// Package p2p is hidage's networking layer between friends: a libp2p host whose
// peer identity is the node's Ed25519 key, with request/response protocols to
// push and fetch encrypted objects (chunks, DAG nodes, snapshots, shards) by
// content address, prove storage (audits), hand over and return signed head
// pointers, and exchange signed address records so friends whose addresses
// change can still be found. There is no global discovery: a node talks to the
// friends it knows. Objects on the wire are already encrypted/signed.
package p2p

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

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
	sink       Sink
	acceptPush func(senderPub []byte, cumulative int64) bool
	onStored   func(senderPub []byte, bytes int64)
}

// New starts a libp2p host whose peer ID is derived from id (the node's Ed25519
// identity), listening on listen (multiaddrs; empty = ephemeral TCP and QUIC
// ports on all interfaces). It serves objects from blobs.
//
// Every node tries to be reachable across NATs: UPnP/NAT-PMP port mapping, the
// circuit-relay transport (to dial friends through a relay) and DCUtR hole
// punching (to upgrade relayed connections to direct ones). Options add the relay
// service and relay reservations; see connectivity.go.
func New(id identity.Identity, blobs Blobs, listen []string, opts ...Option) (*Node, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	priv, err := crypto.UnmarshalEd25519PrivateKey(id.Private)
	if err != nil {
		return nil, fmt.Errorf("convert identity key: %w", err)
	}
	if len(listen) == 0 {
		listen = []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic-v1"}
	}
	lopts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(listen...),
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
	}
	if o.allowInbound != nil {
		lopts = append(lopts, libp2p.ConnectionGater(inboundGater{allow: o.allowInbound}))
	}
	lopts = append(lopts, o.connectivity()...)
	h, err := libp2p.New(lopts...)
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

// ListenAddrs returns the node's addresses worth handing to a friend (without
// the /p2p/<id> suffix): loopback and link-local addresses are dropped unless
// nothing else is available.
func (n *Node) ListenAddrs() []string {
	return ShareableAddrs(n.host.Addrs())
}

// ShareableAddrs filters addrs down to those reachable from another machine,
// falling back to all of them if none are.
func ShareableAddrs(addrs []multiaddr.Multiaddr) []string {
	var out, all []string
	for _, a := range addrs {
		all = append(all, a.String())
		if !manet.IsIPLoopback(a) && !manet.IsIP6LinkLocal(a) {
			out = append(out, a.String())
		}
	}
	if len(out) == 0 {
		return all
	}
	return out
}

// ResolveListenAddrs expands unspecified listen addresses (0.0.0.0, ::) over this
// machine's interfaces, for describing a node that is not currently running.
func ResolveListenAddrs(listen []string) []string {
	ifaces, err := manet.InterfaceMultiaddrs()
	if err != nil {
		return nil
	}
	var resolved []multiaddr.Multiaddr
	for _, l := range listen {
		ma, err := multiaddr.NewMultiaddr(l)
		if err != nil {
			continue
		}
		rs, err := manet.ResolveUnspecifiedAddress(ma, ifaces)
		if err != nil {
			continue
		}
		resolved = append(resolved, rs...)
	}
	return ShareableAddrs(resolved)
}

// ConnectAddrs connects to a peer known by ID and a list of addresses (without
// the /p2p suffix).
func (n *Node) ConnectAddrs(ctx context.Context, pid peer.ID, addrs []string) error {
	info := peer.AddrInfo{ID: pid}
	for _, a := range addrs {
		if ma, err := multiaddr.NewMultiaddr(a); err == nil {
			info.Addrs = append(info.Addrs, ma)
		}
	}
	if len(info.Addrs) == 0 {
		return fmt.Errorf("no known address for %s", pid)
	}
	return n.host.Connect(ctx, info)
}

// Connected reports whether the node has a live connection to p.
func (n *Node) Connected(p peer.ID) bool {
	return n.host.Network().Connectedness(p) == network.Connected
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

// PeerPubKeyHex returns the hex Ed25519 public key for a peer ID (the key is
// inlined in the ID), matching the format used by the friend list and reputation store.
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
