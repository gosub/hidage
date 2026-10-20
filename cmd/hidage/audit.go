package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/friends"
	"hidage/internal/p2p"
	"hidage/internal/pack"
	"hidage/internal/repo"
	"hidage/internal/store"
)

// auditSample is how many of a peer's shards to challenge per audit run.
const auditSample = 4

func newAuditCmd() *cobra.Command {
	var peers []string
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Challenge the friends holding this repo's shards to prove they still have them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kr, err := unlock()
			if err != nil {
				return err
			}
			folder := repo.Find(startDir())
			if folder == "" {
				return fmt.Errorf("not inside a hidage repository")
			}
			r, err := repo.Open(folder, kr.MasterKey(), config.DataStoreDir())
			if err != nil {
				return err
			}
			dist, err := readDistribution(r.HidageFile("distribution.json"))
			if err != nil {
				return fmt.Errorf("nothing published from this repo yet; run `hidage publish` first")
			}

			// Regenerate the published shards deterministically so we know the
			// exact bytes each peer should be able to prove it holds. Audit what
			// was published, even if newer snapshots were taken since.
			var snap *repo.Snapshot
			if dist.SnapshotID != "" {
				snap, err = r.FindSnapshot(dist.SnapshotID)
			} else {
				snap, err = r.Latest() // distribution written before snapshot ids were recorded
			}
			if err != nil || snap == nil {
				return fmt.Errorf("the published snapshot is no longer here (forgotten?); re-publish before auditing")
			}
			objs, err := r.PublishSet(snap)
			if err != nil {
				return err
			}
			rel, shardBlobs, err := pack.Build(objs, dist.Redundancy, snap.ID)
			if err != nil {
				return err
			}
			relBytes, _ := json.Marshal(rel)
			if store.ID(relBytes) != dist.ReleaseID {
				return fmt.Errorf("the published release can't be regenerated; re-publish before auditing")
			}

			node, err := p2p.New(kr.Identity(), nil, nil)
			if err != nil {
				return err
			}
			defer node.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			book, err := loadFriends()
			if err != nil {
				return err
			}
			// Whom to audit: the named peers, or every friend holding shards.
			var targets []peer.ID
			if len(peers) > 0 {
				for _, addr := range peers {
					pid, err := node.Connect(ctx, addr)
					if err != nil {
						fmt.Printf("  %s: %v\n", addr, err)
						continue
					}
					targets = append(targets, pid)
				}
			} else {
				reached := reachFriends(ctx, node, book, true)
				for pubHex := range dist.Peers {
					name := pubHex[:12]
					if f, ok := book.Get(pubHex); ok {
						name = f.Nickname
					}
					if !seenFriend(reached, pubHex) {
						fmt.Printf("  %s: unreachable, not audited\n", name)
						continue
					}
					pid, _ := friends.PeerIDFromHex(pubHex)
					targets = append(targets, pid)
				}
			}

			for _, pid := range targets {
				pubHex, _ := p2p.PeerPubKeyHex(pid)
				name := peerName(book, peerID{id: pid, pubHex: pubHex})
				shardIDs := dist.Peers[pubHex]
				if len(shardIDs) == 0 {
					fmt.Printf("  %s: holds no shards of this backup\n", name)
					continue
				}
				sample := sampleShards(shardIDs, auditSample)
				pass := 0
				for _, id := range sample {
					rec, err := node.Audit(ctx, pid, id, shardBlobs[id])
					if err == nil && rec.OK {
						pass++
					}
				}
				result := &friends.Audit{At: time.Now().UTC(), Sampled: len(sample), Passed: pass}
				book.Update(pubHex, func(f *friends.Friend) error { f.LastAudit = result; return nil })
				verdict := "PASS"
				if !result.OK() {
					verdict = "FAIL: they no longer hold all of your data; re-publish and talk to them"
				}
				fmt.Printf("  %s: %d/%d sampled shards proven held → %s\n", name, pass, len(sample), verdict)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&peers, "peer", nil, "audit only this peer multiaddr (default: every friend holding shards)")
	return cmd
}

// sampleShards returns up to n randomly-chosen shard ids.
func sampleShards(ids []string, n int) []string {
	if len(ids) <= n {
		return ids
	}
	cp := append([]string(nil), ids...)
	rand.Shuffle(len(cp), func(i, j int) { cp[i], cp[j] = cp[j], cp[i] })
	return cp[:n]
}
