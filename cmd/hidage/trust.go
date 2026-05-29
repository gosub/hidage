package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/trust"
)

func loadPolicy() (*trust.Policy, error) {
	paths, err := config.Ensure()
	if err != nil {
		return nil, err
	}
	return trust.Load(paths.TrustPath)
}

func newTrustCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Manage the peer trust policy (whitelist / blocklist)",
	}
	cmd.AddCommand(
		trustActionCmd("add", "Trust a peer's public key (bypass reputation)",
			func(p *trust.Policy, k string) error { return p.Trust(k) }),
		trustActionCmd("remove", "Stop trusting a peer's public key",
			func(p *trust.Policy, k string) error { return p.Remove(k) }),
		trustActionCmd("block", "Block a peer's public key (never interact)",
			func(p *trust.Policy, k string) error { return p.Block(k) }),
		trustActionCmd("unblock", "Unblock a peer's public key",
			func(p *trust.Policy, k string) error { return p.Unblock(k) }),
		trustListCmd(),
	)
	return cmd
}

func trustActionCmd(use, short string, action func(*trust.Policy, string) error) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <pubkey-hex>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := loadPolicy()
			if err != nil {
				return err
			}
			if err := action(p, args[0]); err != nil {
				return err
			}
			fmt.Printf("%s: %s\n", use, args[0])
			return nil
		},
	}
}

func trustListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List trusted and blocked public keys",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := loadPolicy()
			if err != nil {
				return err
			}
			trusted, blocked := p.List()
			fmt.Printf("Trusted (%d):\n", len(trusted))
			for _, k := range trusted {
				fmt.Printf("  %s\n", k)
			}
			fmt.Printf("Blocked (%d):\n", len(blocked))
			for _, k := range blocked {
				fmt.Printf("  %s\n", k)
			}
			return nil
		},
	}
}
