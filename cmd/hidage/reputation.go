package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"github.com/spf13/cobra"

	"hidage/internal/accounting"
	"hidage/internal/config"
	"hidage/internal/p2p"
	"hidage/internal/pack"
	"hidage/internal/repo"
	"hidage/internal/reputation"
	"hidage/internal/store"
)

// auditSample is how many of a peer's shards to challenge per audit run.
const auditSample = 4

func newReputationCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reputation",
		Short: "Show per-peer audit reputation scores",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.Ensure()
			if err != nil {
				return err
			}
			rep, err := reputation.Load(paths.ReputationPath)
			if err != nil {
				return err
			}
			entries := rep.List()
			if len(entries) == 0 {
				fmt.Println("(no audit history yet; run `hidage audit`)")
				return nil
			}
			fmt.Printf("%-64s  %6s  %6s  %6s  %s\n", "PUBKEY", "PASS", "FAIL", "SCORE", "GOOD")
			for _, e := range entries {
				fmt.Printf("%-64s  %6.1f  %6.1f  %6.2f  %v\n", e.PubKey, e.Passes, e.Fails, e.Score, e.Good)
			}
			return nil
		},
	}
}

func newAuditCmd() *cobra.Command {
	var bootstrap []string
	var peers []string
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Challenge storage peers to prove they hold your shards; update reputation + accounting",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kr, err := unlock()
			if err != nil {
				return err
			}
			paths, err := config.Ensure()
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
			// exact bytes each peer should be able to prove it holds.
			snap, err := r.Latest()
			if err != nil || snap == nil {
				return fmt.Errorf("no snapshot to audit")
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
				return fmt.Errorf("the snapshot changed since it was published; re-publish before auditing")
			}

			rep, err := reputation.Load(paths.ReputationPath)
			if err != nil {
				return err
			}
			ledger, err := accounting.Load(paths.AccountingPath)
			if err != nil {
				return err
			}

			node, err := p2p.New(kr.Identity(), nil, "")
			if err != nil {
				return err
			}
			defer node.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := node.EnableDHT(ctx); err != nil {
				return err
			}
			if err := joinNetwork(ctx, node, bootstrap); err != nil {
				return err
			}

			for _, addr := range peers {
				pid, err := node.Connect(ctx, addr)
				if err != nil {
					fmt.Printf("  %s: %v\n", addr, err)
					continue
				}
				pubHex, _ := p2p.PeerPubKeyHex(pid)
				shardIDs := dist.Peers[pubHex]
				if len(shardIDs) == 0 {
					fmt.Printf("  %s: no shards assigned to this peer\n", pid)
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
				ok := pass == len(sample)
				rep.Record(pubHex, ok)
				verdict, note := "PASS", ""
				if !ok {
					verdict = "FAIL"
					note = ", storage credit revoked"
					ledger.RevokeTheyStoreForUs(pubHex) // it dropped our data; revoke its credit
				}
				fmt.Printf("  %s: %d/%d sampled shards verified → %s (score %.2f)%s\n",
					pid, pass, len(sample), verdict, rep.Score(pubHex), note)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&bootstrap, "bootstrap", nil, "bootstrap peer multiaddr(s) (default: your friends)")
	cmd.Flags().StringArrayVar(&peers, "peer", nil, "storage peer multiaddr(s) to audit")
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
