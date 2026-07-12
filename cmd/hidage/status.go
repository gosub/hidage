package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/reputation"
)

// newStatusCmd reports, for the current repository, whether its data is
// protected and how protected: snapshot freshness, whether the latest snapshot
// has been published, how many peers hold shards, the current fault tolerance,
// and any peers currently failing audits. It exits non-zero when the backup is
// degraded, so it composes with cron/monitoring.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the current repository's backup is protected, and how protected",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := discoverRepoNoKey() // reads only signed metadata + local state
			if err != nil {
				return err
			}
			snaps, err := r.ListSnapshots()
			if err != nil {
				return err
			}
			fmt.Printf("Repository %s  (%s)\n", r.RepoID()[:12], r.Folder)

			degraded := false
			warn := func(format string, a ...any) {
				degraded = true
				fmt.Printf("  ! "+format+"\n", a...)
			}

			if len(snaps) == 0 {
				warn("no snapshots yet — nothing is backed up (run `hidage snapshot`)")
				return exitCode(degraded)
			}
			latest := snaps[len(snaps)-1]
			fmt.Printf("  snapshots:     %d (latest %s, %s ago)\n",
				len(snaps), latest.ID[:12], humanAge(time.Since(latest.CreatedAt)))

			dist, err := readDistribution(r.HidageFile("distribution.json"))
			if err != nil {
				warn("never published — the backup exists only on this machine (run `hidage publish`)")
				return exitCode(degraded)
			}

			switch {
			case dist.SnapshotID == "":
				fmt.Printf("  published:     yes (release %s)\n", short(dist.ReleaseID))
			case dist.SnapshotID == latest.ID:
				fmt.Printf("  published:     up to date (%s ago)\n", humanAge(time.Since(dist.PublishedAt)))
			default:
				warn("latest snapshot is NOT published — publish %s is stale (run `hidage publish`)", short(dist.SnapshotID))
			}

			// Fault tolerance: how many peers can be lost and still reconstruct.
			nPeers := len(dist.Peers)
			if nPeers == 0 {
				warn("no peers hold shards — the published backup has no redundancy")
				return exitCode(degraded)
			}

			rep, err := reputation.Load(mustConfig().ReputationPath)
			if err != nil {
				return err
			}
			tolerate, failing := assessPeers(dist, rep)
			fmt.Printf("  peers:         %d holding shards (%.1fx redundancy)\n", nPeers, dist.Redundancy)
			for pubHex := range dist.Peers {
				score, samples := rep.Evidence(pubHex)
				state := "unproven"
				switch {
				case rep.Bad(pubHex):
					state = "FAILING"
				case rep.Good(pubHex):
					state = "good"
				}
				fmt.Printf("    %s  %-8s score %.2f (%.1f audits)\n", short(pubHex), state, score, samples)
			}

			if tolerate == 0 {
				warn("no fault tolerance — losing any one peer loses the backup")
			} else {
				fmt.Printf("  tolerance:     can lose up to %d of %d peers\n", tolerate, nPeers)
			}
			if failing > tolerate {
				warn("%d peer(s) are failing audits but tolerance is only %d — data may be unrecoverable", failing, tolerate)
			} else if failing > 0 {
				warn("%d peer(s) are failing audits — re-publish to restore redundancy", failing)
			}
			return exitCode(degraded)
		},
	}
}

// peerRep is the slice of a reputation store status needs: satisfied by
// *reputation.Store, and by a fake in tests.
type peerRep interface {
	Bad(pubHex string) bool
}

// assessPeers computes, for a published distribution, how many peers can be lost
// while still reconstructing (tolerate) and how many are currently failing audits
// (failing). When failing exceeds tolerate the backup may be unrecoverable.
func assessPeers(dist distribution, rep peerRep) (tolerate, failing int) {
	nPeers := len(dist.Peers)
	if dist.Redundancy > 0 {
		tolerate = int(float64(nPeers) * (1 - 1/dist.Redundancy))
	}
	for pubHex := range dist.Peers {
		if rep.Bad(pubHex) {
			failing++
		}
	}
	return tolerate, failing
}

// exitCode returns an error (non-zero exit) when the backup is degraded, so
// status is usable in scripts, without printing a second error line.
func exitCode(degraded bool) error {
	if degraded {
		return errSilentDegraded
	}
	return nil
}

// errSilentDegraded signals a degraded backup as a non-zero exit; main prints
// "error:" for real failures, so this carries an empty message.
var errSilentDegraded = &degradedError{}

type degradedError struct{}

func (*degradedError) Error() string { return "" }

func mustConfig() config.Paths {
	p, _ := config.Resolve()
	return p
}

func short(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// humanAge renders a duration as a coarse, readable age (reads with "ago").
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "moments"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
