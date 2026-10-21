package p2p

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"hidage/internal/hosting"
	"hidage/internal/identity"
	"hidage/internal/store"
)

func newNodeOpts(t *testing.T, blobs Blobs, opts ...Option) *Node {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(id, blobs, []string{"/ip4/127.0.0.1/tcp/0"}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}

func addrInfo(t *testing.T, n *Node) peer.AddrInfo {
	t.Helper()
	ai := peer.AddrInfo{ID: n.ID()}
	for _, a := range n.ListenAddrs() {
		ai.Addrs = append(ai.Addrs, multiaddr.StringCast(a))
	}
	return ai
}

// A node behind a NAT (forced private) reserves a slot on a friend's relay and
// advertises a circuit address; another friend reaches it only through that
// address, and the relayed connection carries ordinary hidage protocols.
func TestRelayedFriend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	defer func(d time.Duration) { relayRetryInterval = d }(relayRetryInterval)
	relayRetryInterval = time.Second

	var relay, hidden, owner *Node
	friends := map[peer.ID]bool{}
	isFriend := func(p peer.ID) bool { return friends[p] }

	relay = newNodeOpts(t, nil, WithReachability(ReachabilityPublic), WithRelayService(isFriend), WithInboundFilter(isFriend))
	hidden = newNodeOpts(t, nil, WithReachability(ReachabilityPrivate), WithInboundFilter(isFriend),
		WithRelayCandidates(func() []peer.AddrInfo { return []peer.AddrInfo{addrInfo(t, relay)} }))
	hosted, _ := hosting.Open(t.TempDir())
	hidden.EnableHosting(hosted, func(string) int64 { return 1 << 20 }, nil)
	owner = newNodeOpts(t, nil)
	friends[relay.ID()], friends[hidden.ID()], friends[owner.ID()] = true, true, true

	// libp2p only advertises circuit addresses through relays with public IPs, so
	// on loopback build the address by hand. Dialing it succeeds once the hidden
	// node holds a reservation on the relay.
	circuit := relay.ListenAddrs()[0] + "/p2p/" + relay.ID().String() + "/p2p-circuit"
	for {
		c, cancelDial := context.WithTimeout(ctx, 2*time.Second)
		err := owner.ConnectAddrs(c, hidden.ID(), []string{circuit})
		cancelDial()
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("dial through relay: %v", err)
		case <-time.After(500 * time.Millisecond):
		}
	}
	if conns := owner.host.Network().ConnsToPeer(hidden.ID()); len(conns) == 0 ||
		!strings.Contains(conns[0].RemoteMultiaddr().String(), "p2p-circuit") {
		t.Fatalf("expected a relayed connection, got %v", conns)
	}
	objs := map[string][]byte{store.ID([]byte("relayed")): []byte("relayed")}
	if res, err := owner.PushSet(ctx, hidden.ID(), "0123456789abcdef0123456789abcdef", objs); err != nil || res.Sent != 1 {
		t.Fatalf("push over relay: %+v, %v", res, err)
	}
	for id := range objs {
		if got, err := owner.FetchObject(ctx, hidden.ID(), id); err != nil || string(got) != "relayed" {
			t.Fatalf("fetch over relay: %q, %v", got, err)
		}
	}

	// A stranger may not use the relay to reach the hidden node.
	stranger := newNodeOpts(t, nil)
	if err := stranger.ConnectAddrs(ctx, hidden.ID(), []string{circuit}); err == nil {
		if _, err := stranger.FetchObject(ctx, hidden.ID(), "00"); err == nil {
			t.Fatal("stranger reached the hidden node through the relay")
		}
	}
}
