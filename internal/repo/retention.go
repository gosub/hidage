package repo

import (
	"fmt"
	"sort"
	"time"
)

// Retention says which snapshots to keep: the newest KeepLast, plus the newest
// snapshot of each of the last KeepDaily days and KeepWeekly weeks that have
// one. The newest snapshot is always kept.
type Retention struct {
	KeepLast   int
	KeepDaily  int
	KeepWeekly int
}

// Retained returns the ids of the snapshots the policy keeps, given all of
// them. Days and weeks are calendar days and ISO weeks in local time, counted
// back over the snapshots that exist (a week with no snapshot uses no slot).
func (p Retention) Retained(snaps []*Snapshot) map[string]bool {
	sorted := append([]*Snapshot(nil), snaps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].CreatedAt.After(sorted[j].CreatedAt) })
	keep := map[string]bool{}
	if len(sorted) == 0 {
		return keep
	}
	keep[sorted[0].ID] = true
	for i := 0; i < p.KeepLast && i < len(sorted); i++ {
		keep[sorted[i].ID] = true
	}
	bucket := func(n int, key func(time.Time) string) {
		seen := map[string]bool{}
		for _, s := range sorted {
			if len(seen) >= n {
				return
			}
			k := key(s.CreatedAt.Local())
			if !seen[k] {
				seen[k] = true
				keep[s.ID] = true // the newest snapshot of that period
			}
		}
	}
	bucket(p.KeepDaily, func(t time.Time) string { return t.Format("2006-01-02") })
	bucket(p.KeepWeekly, func(t time.Time) string {
		y, w := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	})
	return keep
}
