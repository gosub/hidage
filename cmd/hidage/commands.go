package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/dag"
	"hidage/internal/p2p"
	"hidage/internal/repo"
)

func newBackupCmd() *cobra.Command {
	var local, replace, once bool
	var opts publishOpts
	cmd := &cobra.Command{
		Use:   "backup [folder]",
		Short: "Back up a folder now: snapshot it and send what changed to your friends",
		Long: `Snapshot a folder (creating its .hidage repository on first use) and publish it
to your friends: the snapshot is erasure-coded and spread across every friend you can
reach, sending only what they don't already hold.

The folder is also added to the folders "hidage daemon" keeps backed up (unless
--once or --local). Without a folder argument, backs up the repository you are in.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := startDir()
			if len(args) == 1 {
				target = args[0]
			}
			kr, err := unlock()
			if err != nil {
				return err
			}
			mk, dataDir := kr.MasterKey(), config.DataStoreDir()

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
				fmt.Printf("Created repository in %s/.hidage\n", r.Folder)
			}

			var snap *repo.Snapshot
			var stats *repo.SnapshotStats
			if replace {
				snap, stats, err = r.Snapshot(kr.Identity().Private, true)
			} else {
				snap, stats, err = r.SnapshotIfChanged(kr.Identity().Private)
			}
			if err != nil {
				return err
			}
			switch {
			case stats.Unchanged:
				fmt.Printf("No changes since snapshot %s of %s.\n", snap.ID[:12], r.Folder)
			case replace && stats.Pruned > 0:
				fmt.Printf("Snapshot %s of %s: %d new objects, replaced the previous one (pruned %d)\n", snap.ID[:12], r.Folder, stats.NewObjects, stats.Pruned)
			default:
				fmt.Printf("Snapshot %s of %s: %d new objects\n", snap.ID[:12], r.Folder, stats.NewObjects)
			}
			if n := len(stats.Skipped); n > 0 {
				fmt.Printf("WARNING: skipped %d unsupported entr%s (symlinks/devices/etc.), not backed up:\n",
					n, plural(n, "y", "ies"))
				for _, p := range stats.Skipped {
					fmt.Printf("  %s\n", p)
				}
			}
			if !local && !once {
				paths, err := config.Ensure()
				if err != nil {
					return err
				}
				if added, err := config.AddFolder(paths, r.Folder); err != nil {
					fmt.Printf("note: could not add it to config.toml: %v\n", err)
				} else if added {
					fmt.Println("Added to `folders` in config.toml: `hidage daemon` keeps it backed up.")
				}
			}
			if local {
				return nil
			}

			book, err := loadFriends()
			if err != nil {
				return err
			}
			if len(opts.peers) == 0 && len(book.List()) == 0 {
				fmt.Println("Saved on this machine only. To keep a copy with friends, invite one:")
				fmt.Println("  hidage friend invite")
				return nil
			}
			settings, _ := config.Load()
			if !cmd.Flags().Changed("redundancy") {
				opts.redundancy = settings.Backup.Redundancy
			}
			if !cmd.Flags().Changed("min-friends") {
				opts.minPeers = settings.Backup.MinFriends
			}
			opts.retention = retentionPolicy(settings)
			node, err := p2p.New(kr.Identity(), nil, nil)
			if err != nil {
				return err
			}
			defer node.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			return publishRepo(ctx, node, kr, r, book, opts, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&local, "local", false, "only take the snapshot; don't send it to friends")
	cmd.Flags().BoolVar(&replace, "replace", false, "supersede the previous snapshot instead of keeping it in history")
	cmd.Flags().BoolVar(&once, "once", false, "don't add the folder to the ones the daemon backs up")
	cmd.Flags().StringArrayVar(&opts.peers, "peer", nil, "send to this peer multiaddr instead of your friends")
	cmd.Flags().IntVar(&opts.minPeers, "min-friends", 0, "fail unless at least this many friends are reachable (default: backup.min_friends)")
	cmd.Flags().Float64Var(&opts.redundancy, "redundancy", 0, "shards per shard needed, e.g. 2.0 survives losing half (default: backup.redundancy)")
	return cmd
}

// plural returns one or other by count, for readable messages.
func plural(n int, one, other string) string {
	if n == 1 {
		return one
	}
	return other
}

func newRestoreCmd() *cobra.Command {
	var to string
	var force, list, fromFriends, all, filesOnly bool
	var from []string
	var repoSel string
	cmd := &cobra.Command{
		Use:   "restore [snapshot|latest] [path]",
		Short: "Get files back: a whole snapshot or one path, from here or from your friends",
		Long: `Restore files from this repository's history, or from your friends.

  hidage restore --to ~/out                   the latest snapshot, into ~/out/<folder>
  hidage restore 3f2a1c sub/notes.txt --to .  one file of an older snapshot, into ./notes.txt
  hidage restore --list [snapshot] [path]     see what a snapshot contains
  hidage restore --friends --to ~/out         the latest version of your backups, from your
                                              friends (e.g. after losing this machine; add
                                              --from <a friend's card> on a new one)

Snapshot ids may be shortened to any unambiguous prefix.`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromFriends || len(from) > 0 {
				if len(args) > 0 {
					return errors.New("restoring from friends always takes the latest published version; drop the snapshot/path arguments")
				}
				if to == "" {
					return errors.New("say where to restore with --to <dir>")
				}
				kr, err := unlock()
				if err != nil {
					return err
				}
				return restoreFromFriends(kr, to, from, repoSel, all, filesOnly)
			}

			r, err := discoverRepo()
			if err != nil {
				return err
			}
			sel, path := "latest", ""
			if len(args) > 0 {
				sel = args[0]
			}
			if len(args) > 1 {
				path = args[1]
			}
			snap, err := findSnapshot(r, sel)
			if err != nil {
				return err
			}
			l := r.Loader()
			if list {
				return listSnapshot(l, snap, path)
			}
			if to == "" {
				return errors.New("say where to restore with --to <dir> (or use --list to look first)")
			}
			// Refuse to overwrite unless asked.
			dest := filepath.Join(to, filepath.Base(path))
			if path == "" {
				name, err := repo.RootName(l, snap)
				if err != nil {
					return err
				}
				dest = filepath.Join(to, name)
			}
			if _, err := os.Stat(dest); err == nil && !force {
				return fmt.Errorf("%s already exists; restore somewhere else or pass --force to overwrite", dest)
			}
			if err := repo.RestorePath(l, snap, path, to); err != nil {
				return err
			}
			if path == "" {
				fmt.Printf("Restored snapshot %s into %s\n", snap.ID[:12], dest)
			} else {
				fmt.Printf("Restored %s from snapshot %s into %s\n", path, snap.ID[:12], dest)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "directory to restore into")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	cmd.Flags().BoolVar(&list, "list", false, "list the snapshot's contents instead of restoring")
	cmd.Flags().BoolVar(&fromFriends, "friends", false, "restore the latest published version from your friends")
	cmd.Flags().StringArrayVar(&from, "from", nil, "with --friends: also ask this friend (contact card or /p2p address), e.g. on a new machine")
	cmd.Flags().StringVar(&repoSel, "repo", "", "with --friends: which backup (name or repo-id prefix)")
	cmd.Flags().BoolVar(&all, "all", false, "with --friends: every backup in your account")
	cmd.Flags().BoolVar(&filesOnly, "files-only", false, "with --friends: restore files without making the folders repositories again")
	return cmd
}

// findSnapshot resolves "latest" or an id prefix.
func findSnapshot(r *repo.Repo, sel string) (*repo.Snapshot, error) {
	if sel == "latest" {
		snap, err := r.Latest()
		if err == nil && snap == nil {
			err = errors.New("no snapshots yet (run `hidage backup`)")
		}
		return snap, err
	}
	return r.FindSnapshot(sel)
}

// listSnapshot prints the entries of a directory (or a file) in a snapshot.
func listSnapshot(l *dag.Loader, snap *repo.Snapshot, path string) error {
	entries, err := repo.List(l, snap, path)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name
		if e.Kind == dag.KindTree {
			name += "/"
		}
		modified := ""
		if e.ModTime != 0 {
			modified = time.Unix(0, e.ModTime).Local().Format("2006-01-02 15:04")
		}
		fmt.Printf("%-16s  %s\n", modified, name)
	}
	return nil
}

func newLogCmd() *cobra.Command {
	var diff []string
	var fromFriends bool
	var from []string
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show this folder's backup history (or what changed, or what your friends hold)",
		Long: `List this repository's snapshots, newest first, marking the one published to friends.

  hidage log --diff <a> <b>   what changed between two snapshots (+ added, - removed, ~ modified)
  hidage log --friends        the backups your friends hold for you, across all folders`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromFriends || len(from) > 0 {
				kr, err := unlock()
				if err != nil {
					return err
				}
				return listFromFriends(kr, from)
			}
			if len(diff) > 0 {
				if len(diff) != 2 {
					return errors.New("--diff takes two snapshots: --diff <a> --diff <b>, or --diff <a>,<b>")
				}
				return showDiff(diff[0], diff[1])
			}
			r, err := discoverRepoNoKey() // listing reads only signed metadata
			if err != nil {
				return err
			}
			snaps, err := r.ListSnapshots()
			if err != nil {
				return err
			}
			if len(snaps) == 0 {
				fmt.Println("(no snapshots yet; run `hidage backup`)")
				return nil
			}
			published := ""
			if dist, err := readDistribution(r.HidageFile("distribution.json")); err == nil {
				published = dist.SnapshotID
			}
			for i := len(snaps) - 1; i >= 0; i-- {
				s := snaps[i]
				mark := ""
				if s.ID == published {
					mark = "  ← with your friends"
				}
				fmt.Printf("%s  %s%s\n", s.ID[:12], s.CreatedAt.Local().Format("2006-01-02 15:04:05"), mark)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&diff, "diff", nil, "show what changed between two snapshots")
	cmd.Flags().BoolVar(&fromFriends, "friends", false, "list the backups your friends hold for you")
	cmd.Flags().StringArrayVar(&from, "from", nil, "with --friends: also ask this friend (contact card or /p2p address)")
	return cmd
}

func showDiff(selA, selB string) error {
	r, err := discoverRepo()
	if err != nil {
		return err
	}
	a, err := findSnapshot(r, selA)
	if err != nil {
		return err
	}
	b, err := findSnapshot(r, selB)
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
}

func newForgetCmd() *cobra.Command {
	var retention, dryRun bool
	cmd := &cobra.Command{
		Use:   "forget <snapshot>... | --retention",
		Short: "Delete snapshots from this folder's history and free the space only they used",
		Long: `Delete snapshots from this folder's history and free the space only they used.
With --retention, delete every snapshot the [retention] policy in config.toml doesn't
keep (the daemon does this after each new snapshot). The next backup also drops them
from what your friends hold (after their trash grace period).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := discoverRepo()
			if err != nil {
				return err
			}
			if retention {
				if len(args) > 0 {
					return errors.New("use either snapshot ids or --retention")
				}
				settings, _ := config.Load()
				n, err := applyRetention(r, retentionPolicy(settings), dryRun, os.Stdout)
				if err == nil && n == 0 {
					fmt.Println("Nothing to forget: the retention policy keeps every snapshot.")
				}
				return err
			}
			if len(args) == 0 {
				return errors.New("name the snapshots to forget, or use --retention")
			}
			for _, sel := range args {
				snap, err := r.FindSnapshot(sel)
				if err != nil {
					return err
				}
				if err := r.Forget(snap.ID); err != nil {
					return err
				}
				fmt.Printf("Forgot snapshot %s\n", snap.ID[:12])
			}
			stats, err := r.Prune()
			if err != nil {
				return err
			}
			fmt.Printf("Freed %d objects no remaining snapshot uses (%d kept).\n", stats.Deleted, stats.Kept)
			return nil
		},
	}
	cmd.Flags().BoolVar(&retention, "retention", false, "forget what the [retention] policy doesn't keep")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "with --retention: only show what would be forgotten")
	return cmd
}
