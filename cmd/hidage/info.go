package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"hidage/internal/manifest"
)

func newInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <manifest>",
		Short: "Show details of a manifest and shard availability",
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
			sigStatus := "valid"
			if err := m.Verify(); err != nil {
				sigStatus = "INVALID: " + err.Error()
			}

			present := 0
			for _, ref := range m.Shards {
				ok, err := st.Has(ref.ID)
				if err != nil {
					return err
				}
				if ok {
					present++
				}
			}

			kind := "file"
			if m.IsArchive {
				kind = "folder (tar)"
			}
			fmt.Printf("Name:            %s (%s)\n", m.OriginalName, kind)
			fmt.Printf("Created:         %s\n", m.CreatedAt.Format("2006-01-02 15:04:05 MST"))
			fmt.Printf("Owner pubkey:    %x\n", m.OwnerPubKey)
			fmt.Printf("Signature:       %s\n", sigStatus)
			fmt.Printf("Plaintext size:  %d bytes\n", m.PlaintextSize)
			fmt.Printf("Plaintext hash:  %x\n", m.PlaintextSHA256)
			fmt.Printf("Encryption:      %s\n", m.Crypto.Algorithm)
			fmt.Printf("Fountain code:   %s\n", m.Fountain.Algorithm)
			fmt.Printf("  source symbols (min needed): %d\n", m.Fountain.SourceSymbols)
			fmt.Printf("  total shards:                %d\n", m.Fountain.TotalShards)
			fmt.Printf("  shards present locally:      %d\n", present)
			return nil
		},
	}
}
