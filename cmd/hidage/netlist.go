package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"hidage/internal/account"
	"hidage/internal/p2p"
	"hidage/internal/store"
)

func newNetListCmd() *cobra.Command {
	var from []string
	cmd := &cobra.Command{
		Use:   "net-list",
		Short: "List the backups you have published to your friends",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kr, err := unlock()
			if err != nil {
				return err
			}
			node, err := p2p.New(kr.Identity(), nil, nil)
			if err != nil {
				return err
			}
			defer node.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			sources, err := recoverySources(ctx, node, from)
			if err != nil {
				return err
			}
			mid := node.ResolveHead(ctx, sources)
			if mid == "" {
				return fmt.Errorf("none of the %d friend(s) reached holds a backup of yours", len(sources))
			}
			data, err := node.GetObject(ctx, mid, sources)
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
	cmd.Flags().StringArrayVar(&from, "from", nil, "also ask this friend (contact card or /p2p multiaddr), e.g. on a new machine")
	return cmd
}
