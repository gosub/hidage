package p2p

import (
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// Option configures a Node at construction.
type Option func(*options)

type options struct {
	allowInbound    func(peer.ID) bool
	relayFor        func(peer.ID) bool
	relayCandidates func() []peer.AddrInfo
	reachability    Reachability
}

// WithInboundFilter admits inbound connections only from peers for which allow
// returns true (checked once the remote's identity is authenticated). hidage
// uses it to accept connections from friends only. Outbound dials are not
// filtered: a node only dials peers it chose to.
func WithInboundFilter(allow func(peer.ID) bool) Option {
	return func(o *options) { o.allowInbound = allow }
}

// inboundGater is a libp2p ConnectionGater that filters authenticated inbound
// connections by peer ID.
type inboundGater struct {
	allow func(peer.ID) bool
}

func (g inboundGater) InterceptPeerDial(peer.ID) bool                      { return true }
func (g inboundGater) InterceptAddrDial(peer.ID, multiaddr.Multiaddr) bool { return true }
func (g inboundGater) InterceptAccept(network.ConnMultiaddrs) bool         { return true }
func (g inboundGater) InterceptUpgraded(network.Conn) (bool, control.DisconnectReason) {
	return true, 0
}

func (g inboundGater) InterceptSecured(dir network.Direction, id peer.ID, _ network.ConnMultiaddrs) bool {
	return dir != network.DirInbound || g.allow(id)
}
