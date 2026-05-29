package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/p2p"
	"hidage/internal/repo"
	"hidage/internal/reputation"
)

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
			fmt.Printf("%-64s  %5s  %5s  %6s  %s\n", "PUBKEY", "PASS", "FAIL", "SCORE", "GOOD")
			for _, e := range entries {
				fmt.Printf("%-64s  %5d  %5d  %6.2f  %v\n", e.PubKey, e.Passes, e.Fails, e.Score, e.Good)
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
		Short: "Audit storage peers for the latest snapshot's objects; update reputation",
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
			snap, err := r.Latest()
			if err != nil || snap == nil {
				return fmt.Errorf("no snapshot to audit")
			}
			objs, err := r.PublishSet(snap)
			if err != nil {
				return err
			}
			rep, err := reputation.Load(paths.ReputationPath)
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
			if err := node.Bootstrap(ctx, bootstrap); err != nil {
				return err
			}

			for _, addr := range peers {
				pid, err := node.Connect(ctx, addr)
				if err != nil {
					fmt.Printf("  %s: %v\n", addr, err)
					continue
				}
				pass, total := 0, 0
				for id, want := range objs {
					total++
					rec, err := node.Audit(ctx, pid, id, want)
					if err == nil && rec.OK {
						pass++
					}
				}
				ok := pass == total
				pubHex, _ := p2p.PeerPubKeyHex(pid)
				rep.Record(pubHex, ok)
				verdict := "PASS"
				if !ok {
					verdict = "FAIL"
				}
				fmt.Printf("  %s: %d/%d objects verified → %s (score %.2f)\n",
					pid, pass, total, verdict, rep.Score(pubHex))
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&bootstrap, "bootstrap", nil, "bootstrap peer multiaddr(s)")
	cmd.Flags().StringArrayVar(&peers, "peer", nil, "storage peer multiaddr(s) to audit")
	return cmd
}
