package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"hidage/internal/friends"
	"hidage/internal/p2p"
)

// dialTimeout bounds one attempt to reach a friend at its known addresses.
const dialTimeout = 10 * time.Second

// reachFriends connects node to every friend it can and returns those reached.
// It first dials each friend at its stored addresses; for friends that fail, it
// asks the friends already reached for a newer signed address record (the
// friend may have moved) and tries again. Every friend reached is asked for its
// own current record, so the book stays fresh. quiet suppresses the per-friend
// "unreachable" lines.
func reachFriends(ctx context.Context, node *p2p.Node, book *friends.Book, quiet bool) []friends.Friend {
	list := book.List()
	reached := make([]bool, len(list))

	dialAll := func(idx []int) {
		var wg sync.WaitGroup
		for _, i := range idx {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				pid, err := list[i].PeerID()
				if err != nil {
					return
				}
				if node.Connected(pid) {
					reached[i] = true
					return
				}
				c, cancel := context.WithTimeout(ctx, dialTimeout)
				defer cancel()
				reached[i] = node.ConnectAddrs(c, pid, list[i].Addrs) == nil
			}(i)
		}
		wg.Wait()
	}
	all := make([]int, len(list))
	for i := range list {
		all[i] = i
	}
	dialAll(all)

	// Ask reached friends where the unreachable ones are now.
	var retry []int
	for i, ok := range reached {
		if ok {
			continue
		}
		for j, okj := range reached {
			if !okj {
				continue
			}
			pidj, _ := list[j].PeerID()
			if lookupAddrs(ctx, node, book, pidj, list[i].PubKey) {
				list[i], _ = book.Get(list[i].PubKey)
				retry = append(retry, i)
				break
			}
		}
	}
	dialAll(retry)

	var out []friends.Friend
	for i, ok := range reached {
		f := list[i]
		if !ok {
			if !quiet {
				fmt.Printf("  %s: unreachable\n", f.Nickname)
			}
			continue
		}
		pid, _ := f.PeerID()
		lookupAddrs(ctx, node, book, pid, f.PubKey) // the friend's own, freshest record
		book.Update(f.PubKey, func(f *friends.Friend) error { f.LastSeen = time.Now().UTC(); return nil })
		f, _ = book.Get(f.PubKey)
		out = append(out, f)
	}
	return out
}

// lookupAddrs asks peer for subject's signed address record and stores it if it
// is newer than what the book holds. It reports whether the addresses changed.
func lookupAddrs(ctx context.Context, node *p2p.Node, book *friends.Book, from peer.ID, subjectHex string) bool {
	c, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	rec, err := node.FetchAddrRecord(c, from, subjectHex)
	if err != nil || rec == nil {
		return false
	}
	changed, _ := book.ApplyAddrs(subjectHex, rec.Addrs, rec.Seq, rec.Sig)
	return changed
}

// storedAddrRecord returns the signed record the book holds for a friend, for
// passing on to other friends (nil if its addresses came from a card).
func storedAddrRecord(book *friends.Book, subjectHex string) *p2p.AddrRecord {
	f, ok := book.Get(subjectHex)
	if !ok || f.AddrSig == nil {
		return nil
	}
	return &p2p.AddrRecord{PubKey: f.PubKey, Addrs: f.Addrs, Seq: f.AddrSeq, Sig: f.AddrSig}
}

// peerIDs returns the libp2p peer IDs of friends.
func peerIDs(list []friends.Friend) []peer.ID {
	var out []peer.ID
	for _, f := range list {
		if pid, err := f.PeerID(); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

// connectExtra connects to peers given on the command line, each either a
// contact card or a full multiaddr ending in /p2p/<id> (e.g. a friend to recover
// from on a machine with no friend list yet). It returns the peers reached.
func connectExtra(ctx context.Context, node *p2p.Node, targets []string) ([]peer.ID, error) {
	var out []peer.ID
	for _, t := range targets {
		c, cancel := context.WithTimeout(ctx, dialTimeout)
		var pid peer.ID
		var err error
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), friends.CardPrefix) {
			var card *friends.Card
			if card, err = friends.DecodeCard(t); err == nil {
				pid, err = friends.PeerIDFromHex(fmt.Sprintf("%x", card.PubKey))
				if err == nil {
					err = node.ConnectAddrs(c, pid, card.AddrStrings())
				}
			}
		} else {
			pid, err = node.Connect(c, t)
		}
		cancel()
		if err != nil {
			return nil, fmt.Errorf("reach %s: %w", t, err)
		}
		out = append(out, pid)
	}
	return out, nil
}
