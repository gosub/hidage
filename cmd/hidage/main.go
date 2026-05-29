// Command hidage is the CLI for hidden storage: it encrypts, fountain-shards,
// and reassembles files and folders. The MVP keeps shards in a local
// content-addressed store; later phases distribute them across a P2P network.
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
	root.PersistentFlags().StringVar(&repoFlag, "repo", "",
		"repository directory (default: ~/.config/hidage/repo)")
	root.AddCommand(
		newInitCmd(),
		newSnapshotCmd(),
		newSnapshotsCmd(),
		newRestoreCmd(),
		newDiffCmd(),
		newForgetCmd(),
		newPruneCmd(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
