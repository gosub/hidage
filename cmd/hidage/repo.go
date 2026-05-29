package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/dag"
	"hidage/internal/identity"
	"hidage/internal/repo"
)

func newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Versioned, incremental snapshot storage (Phase 2)",
	}
	cmd.AddCommand(
		repoInitCmd(),
		repoSnapshotCmd(),
		repoSnapshotsCmd(),
		repoRestoreCmd(),
		repoDiffCmd(),
		repoForgetCmd(),
		repoPruneCmd(),
	)
	return cmd
}

// loadIdentity returns the node identity, requiring `hidage init` to have run.
func loadIdentity() (identity.Identity, error) {
	paths, err := config.Resolve()
	if err != nil {
		return identity.Identity{}, err
	}
	if !identity.Exists(paths.IdentityPath) {
		return identity.Identity{}, fmt.Errorf("no identity found; run `hidage init` first")
	}
	return identity.Load(paths.IdentityPath)
}

func repoInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init <repo-dir>",
		Short: "Create a new repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pass, err := readPassphrase(true)
			if err != nil {
				return err
			}
			r, err := repo.Init(args[0], pass)
			if err != nil {
				return err
			}
			fmt.Printf("Initialized repository at %s\n", r.Path)
			return nil
		},
	}
}

func repoSnapshotCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "snapshot <repo-dir> <source-path>",
		Short: "Take an incremental snapshot of a file or folder",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := loadIdentity()
			if err != nil {
				return err
			}
			pass, err := readPassphrase(false)
			if err != nil {
				return err
			}
			r, err := repo.Open(args[0], pass)
			if err != nil {
				return err
			}
			snap, stats, err := r.Snapshot(args[1], id.Private)
			if err != nil {
				return err
			}
			fmt.Printf("Snapshot %s\n", snap.ID[:16])
			fmt.Printf("  source:       %s\n", snap.Source)
			if snap.Parent != "" {
				fmt.Printf("  parent:       %s\n", snap.Parent[:16])
			}
			fmt.Printf("  new objects:  %d (stored only the delta)\n", stats.NewObjects)
			fmt.Printf("  total objects: %d\n", stats.TotalObjects)
			return nil
		},
	}
}

func repoSnapshotsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "snapshots <repo-dir>",
		Short: "List snapshot history (oldest first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pass, err := readPassphrase(false)
			if err != nil {
				return err
			}
			r, err := repo.Open(args[0], pass)
			if err != nil {
				return err
			}
			snaps, err := r.ListSnapshots()
			if err != nil {
				return err
			}
			if len(snaps) == 0 {
				fmt.Println("(no snapshots)")
				return nil
			}
			fmt.Printf("%-16s  %-20s  %s\n", "ID", "CREATED", "SOURCE")
			for _, s := range snaps {
				fmt.Printf("%-16s  %-20s  %s\n", s.ID[:16], s.CreatedAt.Format("2006-01-02 15:04:05"), s.Source)
			}
			return nil
		},
	}
}

func repoRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <repo-dir> <snapshot-id> <out-dir>",
		Short: "Restore a snapshot into a directory",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			pass, err := readPassphrase(false)
			if err != nil {
				return err
			}
			r, err := repo.Open(args[0], pass)
			if err != nil {
				return err
			}
			snap, err := r.FindSnapshot(args[1])
			if err != nil {
				return err
			}
			if err := r.Restore(snap, args[2]); err != nil {
				return err
			}
			fmt.Printf("Restored snapshot %s into %s\n", snap.ID[:16], args[2])
			return nil
		},
	}
}

func repoDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <repo-dir> <snapshot-a> <snapshot-b>",
		Short: "Show paths that changed between two snapshots",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			pass, err := readPassphrase(false)
			if err != nil {
				return err
			}
			r, err := repo.Open(args[0], pass)
			if err != nil {
				return err
			}
			a, err := r.FindSnapshot(args[1])
			if err != nil {
				return err
			}
			b, err := r.FindSnapshot(args[2])
			if err != nil {
				return err
			}
			changes, err := r.Diff(a, b)
			if err != nil {
				return err
			}
			if len(changes) == 0 {
				fmt.Println("(no changes)")
				return nil
			}
			sym := map[dag.ChangeKind]string{dag.Added: "+", dag.Removed: "-", dag.Modified: "~"}
			for _, c := range changes {
				fmt.Printf("%s %s\n", sym[c.Kind], c.Path)
			}
			return nil
		},
	}
}

func repoForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <repo-dir> <snapshot-id>",
		Short: "Remove a snapshot record (run prune to reclaim space)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pass, err := readPassphrase(false)
			if err != nil {
				return err
			}
			r, err := repo.Open(args[0], pass)
			if err != nil {
				return err
			}
			if err := r.Forget(args[1]); err != nil {
				return err
			}
			fmt.Println("Forgot snapshot; run `hidage repo prune` to reclaim unreferenced objects.")
			return nil
		},
	}
}

func repoPruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune <repo-dir>",
		Short: "Garbage-collect objects no snapshot references",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pass, err := readPassphrase(false)
			if err != nil {
				return err
			}
			r, err := repo.Open(args[0], pass)
			if err != nil {
				return err
			}
			stats, err := r.Prune()
			if err != nil {
				return err
			}
			fmt.Printf("Pruned: kept %d objects, deleted %d\n", stats.Kept, stats.Deleted)
			return nil
		},
	}
}
