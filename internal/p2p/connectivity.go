package p2p

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/multiformats/go-multiaddr"
)

// Reachability says whether a node can accept connections from the internet.
type Reachability string

const (
	// ReachabilityAuto lets libp2p's AutoNAT decide.
	ReachabilityAuto Reachability = "auto"
	// ReachabilityPublic declares the node reachable (public IP or port forward):
	// it offers the relay service and does not reserve relay slots.
	ReachabilityPublic Reachability = "public"
	// ReachabilityPrivate declares the node behind a NAT: it reserves slots on
	// friends' relays right away and advertises the relayed addresses.
	ReachabilityPrivate Reachability = "private"
)

// ParseReachability validates a reachability setting ("" means auto).
func ParseReachability(s string) (Reachability, error) {
	switch Reachability(s) {
	case "", ReachabilityAuto:
		return ReachabilityAuto, nil
	case ReachabilityPublic, ReachabilityPrivate:
		return Reachability(s), nil
	}
	return "", fmt.Errorf("invalid reachability %q (want auto, public or private)", s)
}

// relayBufferSize is the per-direction buffer of a relayed stream. libp2p's
// default (4 KiB) is sized for hole-punch coordination; friends' relays carry
// backup data, so give them more room.
const relayBufferSize = 64 << 10

// relayRetryInterval is how often a node lacking a relay asks for candidates
// again (a friend's relay service may start after we first tried it).
var relayRetryInterval = 15 * time.Second

// WithRelayService makes the node a circuit relay for the peers allow accepts
// (friends): they may reserve a slot on it and be dialed through it. Relayed
// connections are unlimited in time and data, so they can carry backups when
// hole punching fails. libp2p only runs the service while the node is publicly
// reachable.
func WithRelayService(allow func(peer.ID) bool) Option {
	return func(o *options) { o.relayFor = allow }
}

// WithRelayCandidates lets a node behind a NAT reserve slots on relays drawn
// from candidates (friends that may be reachable), so peers can reach it at a
// /p2p/<relay>/p2p-circuit address.
func WithRelayCandidates(candidates func() []peer.AddrInfo) Option {
	return func(o *options) { o.relayCandidates = candidates }
}

// WithReachability overrides AutoNAT's verdict (see Reachability).
func WithReachability(r Reachability) Option {
	return func(o *options) { o.reachability = r }
}

func (o *options) connectivity() []libp2p.Option {
	var out []libp2p.Option
	switch o.reachability {
	case ReachabilityPublic:
		out = append(out, libp2p.ForceReachabilityPublic())
	case ReachabilityPrivate:
		out = append(out, libp2p.ForceReachabilityPrivate())
	}
	if o.relayFor != nil {
		res := relayv2.DefaultResources()
		res.BufferSize = relayBufferSize
		out = append(out,
			libp2p.EnableNATService(), // let friends learn whether they are reachable
			libp2p.EnableRelayService(
				relayv2.WithResources(res),
				relayv2.WithInfiniteLimits(),
				relayv2.WithACL(friendsACL{allow: o.relayFor}),
			))
	}
	if o.relayCandidates != nil {
		candidates := o.relayCandidates
		out = append(out, libp2p.EnableAutoRelayWithPeerSource(
			func(ctx context.Context, num int) <-chan peer.AddrInfo {
				ch := make(chan peer.AddrInfo, num)
				defer close(ch)
				for _, ai := range candidates() {
					if len(ch) == cap(ch) {
						break
					}
					ch <- ai
				}
				return ch
			},
			autorelay.WithMinCandidates(1),
			autorelay.WithNumRelays(2),
			autorelay.WithBootDelay(0),
			autorelay.WithMinInterval(relayRetryInterval),
			autorelay.WithBackoff(relayRetryInterval),
		))
	}
	return out
}

// friendsACL admits relay reservations and relayed connections only between
// peers the relay accepts.
type friendsACL struct {
	allow func(peer.ID) bool
}

func (a friendsACL) AllowReserve(p peer.ID, _ multiaddr.Multiaddr) bool { return a.allow(p) }

func (a friendsACL) AllowConnect(src peer.ID, _ multiaddr.Multiaddr, dest peer.ID) bool {
	return a.allow(src) && a.allow(dest)
}
