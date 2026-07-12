package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/dag"
	"hidage/internal/repo"
)

func newSnapshotCmd() *cobra.Command {
	var replace bool
	cmd := &cobra.Command{
		Use:   "snapshot [dir]",
		Short: "Snapshot a folder (creates its .hidage repository on first use)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := startDir()
			if len(args) == 1 {
				target = args[0]
			}

			kr, err := unlock()
			if err != nil {
				return err
			}
			mk := kr.MasterKey()
			dataDir := config.DataStoreDir()

			// Use an existing repository found at/above target, else create one
			// rooted at target.
			var r *repo.Repo
			if folder := repo.Find(target); folder != "" {
				if r, err = repo.Open(folder, mk, dataDir); err != nil {
					return err
				}
			} else {
				if r, err = repo.Init(target, mk, dataDir); err != nil {
					return err
				}
				fmt.Printf("Created repository in %s/.hidage\n", target)
			}

			snap, stats, err := r.Snapshot(kr.Identity().Private, replace)
			if err != nil {
				return err
			}
			fmt.Printf("Snapshot %s\n", snap.ID[:16])
			fmt.Printf("  folder:        %s\n", r.Folder)
			if snap.Parent != "" {
				fmt.Printf("  parent:        %s\n", snap.Parent[:16])
			}
			fmt.Printf("  new objects:   %d (stored only the delta)\n", stats.NewObjects)
			if replace && stats.Pruned > 0 {
				fmt.Printf("  replaced previous snapshot; pruned %d objects\n", stats.Pruned)
			}
			fmt.Printf("  total objects: %d\n", stats.TotalObjects)
			if n := len(stats.Skipped); n > 0 {
				fmt.Printf("  WARNING: skipped %d unsupported entr%s (symlinks/devices/etc.), not backed up:\n",
					n, plural(n, "y", "ies"))
				for _, p := range stats.Skipped {
					fmt.Printf("    %s\n", p)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&replace, "replace", false, "supersede the previous snapshot and prune it")
	return cmd
}

// plural returns one or other by count, for readable messages.
func plural(n int, one, other string) string {
	if n == 1 {
		return one
	}
	return other
}

func newSnapshotsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "snapshots",
		Short: "List snapshot history (oldest first)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := discoverRepo()
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

func newRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <snapshot-id> <out-dir>",
		Short: "Restore a snapshot into a directory",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := discoverRepo()
			if err != nil {
				return err
			}
			snap, err := r.FindSnapshot(args[0])
			if err != nil {
				return err
			}
			if err := r.Restore(snap, args[1]); err != nil {
				return err
			}
			fmt.Printf("Restored snapshot %s into %s\n", snap.ID[:16], args[1])
			return nil
		},
	}
}

func newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <snapshot-a> <snapshot-b>",
		Short: "Show paths that changed between two snapshots",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := discoverRepo()
			if err != nil {
				return err
			}
			a, err := r.FindSnapshot(args[0])
			if err != nil {
				return err
			}
			b, err := r.FindSnapshot(args[1])
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

func newForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <snapshot-id>",
		Short: "Remove a snapshot record (run prune to reclaim space)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := discoverRepo()
			if err != nil {
				return err
			}
			if err := r.Forget(args[0]); err != nil {
				return err
			}
			fmt.Println("Forgot snapshot; run `hidage prune` to reclaim unreferenced objects.")
			return nil
		},
	}
}

func newPruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Garbage-collect objects no snapshot references",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := discoverRepo()
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
