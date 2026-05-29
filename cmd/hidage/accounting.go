package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"hidage/internal/accounting"
	"hidage/internal/config"
)

func newAccountingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "accounting",
		Short: "Show per-peer space accounting (stored for them vs. for us)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.Ensure()
			if err != nil {
				return err
			}
			ledger, err := accounting.Load(paths.AccountingPath)
			if err != nil {
				return err
			}
			entries := ledger.List()
			if len(entries) == 0 {
				fmt.Println("(no accounting history yet)")
				return nil
			}
			fmt.Printf("%-64s  %14s  %14s\n", "PUBKEY", "STORED-FOR-THEM", "THEY-STORE-FOR-US")
			for _, e := range entries {
				fmt.Printf("%-64s  %14d  %14d\n", e.PubKey, e.StoredForThem, e.TheyStoreForUs)
			}
			return nil
		},
	}
}
