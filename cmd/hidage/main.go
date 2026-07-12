// Command hidage is the CLI for hidden storage: an encrypted, versioned,
// distributed backup tool. It snapshots folders into a content-addressed
// repository (deterministic encryption + Merkle DAG + signed snapshots), then
// erasure-codes a snapshot into shards spread across libp2p peers, with
// proof-of-storage audits, reputation, and space accounting keeping hosts honest.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:           "hidage",
		Short:         "hidage — encrypted, versioned, distributed backup",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&chdirFlag, "chdir", "C", "",
		"operate as if started in this directory (repository discovery walks up from here)")
	root.AddCommand(
		newInitCmd(),
		newSnapshotCmd(),
		newSnapshotsCmd(),
		newStatusCmd(),
		newRestoreCmd(),
		newDiffCmd(),
		newForgetCmd(),
		newPruneCmd(),
		newRecoveryCodeCmd(),
		newRecoverCmd(),
		newTrustCmd(),
		newServeCmd(),
		newPublishCmd(),
		newNetRestoreCmd(),
		newNetListCmd(),
		newReputationCmd(),
		newAuditCmd(),
		newAccountingCmd(),
	)
	if err := root.Execute(); err != nil {
		// A degraded-backup status is a non-zero exit, not an error to print.
		if _, ok := err.(*degradedError); !ok {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}
