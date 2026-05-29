package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"hidage/internal/manifest"
	"hidage/internal/store"
)

func newVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <manifest>",
		Short: "Check shard availability and per-shard integrity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, _, err := openStore()
			if err != nil {
				return err
			}
			m, err := manifest.Load(args[0])
			if err != nil {
				return err
			}
			if err := m.Verify(); err != nil {
				return fmt.Errorf("manifest signature: %w", err)
			}

			var present, missing, corrupt int
			for _, ref := range m.Shards {
				data, err := st.Get(ref.ID)
				if errors.Is(err, store.ErrNotFound) {
					missing++
					continue
				}
				if err != nil {
					return err
				}
				// Content address is the SHA-256 of the data, so recomputing it
				// detects any corruption.
				if store.ID(data) != ref.ID {
					corrupt++
					continue
				}
				present++
			}

			fmt.Printf("Manifest signature: valid\n")
			fmt.Printf("Shards: %d intact, %d missing, %d corrupt (of %d)\n",
				present, missing, corrupt, len(m.Shards))
			need := m.Fountain.SourceSymbols
			if present < need {
				fmt.Printf("WARNING: only %d intact shards, need ~%d to restore\n", present, need)
				return fmt.Errorf("insufficient intact shards to guarantee restore")
			}
			fmt.Printf("Restorable: yes (%d intact >= ~%d needed)\n", present, need)
			return nil
		},
	}
}
