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

	"hidage/internal/config"
)

// version is set at release time with -ldflags "-X main.buildVersion=v1.2.3".
var buildVersion = "dev"

func main() {
	root := &cobra.Command{
		Use:           "hidage",
		Short:         "hidage: encrypted, versioned backups with your friends",
		Version:       buildVersion,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			_, err := config.Load() // surface a malformed config.toml before doing anything
			return err
		},
	}
	root.PersistentFlags().StringVarP(&chdirFlag, "chdir", "C", "",
		"operate as if started in this directory (repository discovery walks up from here)")
	root.CompletionOptions.HiddenDefaultCmd = true
	root.AddCommand(
		newInitCmd(),
		newIDCmd(),
		newFriendCmd(),
		newBackupCmd(),
		newRestoreCmd(),
		newLogCmd(),
		newForgetCmd(),
		newStatusCmd(),
		newDaemonCmd(),
	)
	if err := root.Execute(); err != nil {
		// A degraded-backup status is a non-zero exit, not an error to print.
		if _, ok := err.(*degradedError); !ok {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}
