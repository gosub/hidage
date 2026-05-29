package dag

import "path"

// ChangeKind classifies a path difference between two trees.
type ChangeKind string

const (
	Added    ChangeKind = "added"
	Removed  ChangeKind = "removed"
	Modified ChangeKind = "modified"
)

// Change is one differing path between two snapshots.
type Change struct {
	Path string
	Kind ChangeKind
}

// Diff compares two tree refs and returns the paths that differ. Subtrees with
// equal refs are skipped wholesale, so the cost is proportional to the change,
// not the tree size.
func (l *Loader) Diff(a, b Ref) ([]Change, error) {
	var changes []Change
	if a.Equal(b) {
		return changes, nil
	}
	if err := l.diffTrees("", a, b, &changes); err != nil {
		return nil, err
	}
	return changes, nil
}

func (l *Loader) diffTrees(prefix string, a, b Ref, out *[]Change) error {
	ta, err := l.Tree(a)
	if err != nil {
		return err
	}
	tb, err := l.Tree(b)
	if err != nil {
		return err
	}
	byName := func(t TreeNode) map[string]Entry {
		m := make(map[string]Entry, len(t.Entries))
		for _, e := range t.Entries {
			m[e.Name] = e
		}
		return m
	}
	ma, mb := byName(ta), byName(tb)

	for name, ea := range ma {
		full := path.Join(prefix, name)
		eb, ok := mb[name]
		if !ok {
			l.markRemoved(full, ea, out)
			continue
		}
		if ea.Ref.Equal(eb.Ref) {
			continue // unchanged subtree/file
		}
		if ea.Kind == KindTree && eb.Kind == KindTree {
			if err := l.diffTrees(full, ea.Ref, eb.Ref, out); err != nil {
				return err
			}
		} else {
			*out = append(*out, Change{Path: full, Kind: Modified})
		}
	}
	for name := range mb {
		if _, ok := ma[name]; !ok {
			*out = append(*out, Change{Path: path.Join(prefix, name), Kind: Added})
		}
	}
	return nil
}

// markRemoved records a removed entry (a single Modified-style line per path).
func (l *Loader) markRemoved(full string, e Entry, out *[]Change) {
	*out = append(*out, Change{Path: full, Kind: Removed})
}

// Reachable returns the set of store CipherIDs reachable from a root tree ref:
// the tree nodes, the file nodes, and the data chunks. Used by prune to decide
// what is safe to garbage-collect.
func (l *Loader) Reachable(root Ref) (map[string]struct{}, error) {
	seen := make(map[string]struct{})
	if err := l.reachTree(root, seen); err != nil {
		return nil, err
	}
	return seen, nil
}

func (l *Loader) reachTree(ref Ref, seen map[string]struct{}) error {
	if _, ok := seen[ref.CipherID]; ok {
		return nil
	}
	seen[ref.CipherID] = struct{}{}
	t, err := l.Tree(ref)
	if err != nil {
		return err
	}
	for _, e := range t.Entries {
		switch e.Kind {
		case KindTree:
			if err := l.reachTree(e.Ref, seen); err != nil {
				return err
			}
		case KindFile:
			if err := l.reachFile(e.Ref, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *Loader) reachFile(ref Ref, seen map[string]struct{}) error {
	if _, ok := seen[ref.CipherID]; ok {
		return nil
	}
	seen[ref.CipherID] = struct{}{}
	f, err := l.File(ref)
	if err != nil {
		return err
	}
	for _, c := range f.Chunks {
		seen[c.CipherID] = struct{}{}
	}
	return nil
}
