package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"hidage/internal/account"
	"hidage/internal/p2p"
	"hidage/internal/store"
)

func newNetListCmd() *cobra.Command {
	var bootstrap []string
	var ownerHex string
	cmd := &cobra.Command{
		Use:   "net-list",
		Short: "List the backups an owner has published to the network",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kr, err := unlock()
			if err != nil {
				return err
			}
			owner := kr.Identity().Public
			if ownerHex != "" {
				raw, err := hex.DecodeString(ownerHex)
				if err != nil || len(raw) != ed25519.PublicKeySize {
					return fmt.Errorf("invalid --owner public key")
				}
				owner = ed25519.PublicKey(raw)
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

			var mid string
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				if mid, _ = node.GetHead(ctx, owner); mid != "" {
					break
				}
				time.Sleep(time.Second)
			}
			if mid == "" {
				return fmt.Errorf("no backups found for that owner")
			}
			data, err := node.GetObject(ctx, mid)
			if err != nil {
				return fmt.Errorf("fetch account manifest: %w", err)
			}
			if store.ID(data) != mid {
				return fmt.Errorf("account manifest failed integrity check")
			}
			manifest, err := account.Parse(data)
			if err != nil {
				return err
			}
			if len(manifest.Entries) == 0 {
				fmt.Println("(no backups)")
				return nil
			}
			fmt.Printf("%-24s  %-12s  %-20s  %s\n", "NAME", "REPO-ID", "UPDATED", "RELEASE")
			for _, e := range manifest.Entries {
				fmt.Printf("%-24s  %-12s  %-20s  %s\n",
					e.Name, e.RepoID[:12], e.UpdatedAt.Format("2006-01-02 15:04:05"), e.ReleaseID[:16])
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&bootstrap, "bootstrap", nil, "bootstrap peer multiaddr(s)")
	cmd.Flags().StringVar(&ownerHex, "owner", "", "owner public key hex (default: your own)")
	return cmd
}
