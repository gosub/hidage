// Package hosting stores what a node keeps on behalf of its friends. Each
// owner (a friend, by hex public key) gets its own directory, so how much space a
// friend uses is simply what is in it, and removing a friend is removing it:
//
//	<root>/<owner>/objects/<id[:2]>/<id>   live objects (shards, releases, manifests)
//	<root>/<owner>/sets/<setID>.json       a set: the ids one backup needs, and when it was last renewed
//	<root>/<owner>/trash/<id[:2]>/<id>     objects no set lists any more
//
// An owner publishes a backup by replacing a whole set: it lists the ids the set
// must contain, sends the ones the host is missing, and the host commits the new
// list. Objects that no set of the owner lists any more move to trash and are
// deleted only after a grace period, so an owner machine that publishes garbage
// (e.g. after ransomware) cannot wipe its remote copies at once. A set that is not
// renewed within the lease is dropped (its owner has gone away).
package hosting

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// ErrNotFound is returned when an owner has no such object.
var ErrNotFound = errors.New("object not found")

// validName bounds owner keys, set ids and object ids to safe path components.
var validName = regexp.MustCompile(`^[0-9a-f]{8,128}$`)

// Entry is one object a set must contain, with its size in bytes.
type Entry struct {
	ID   string
	Size int64
}

type setFile struct {
	IDs       []string  `json:"ids"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Host is the store of friends' data. It is safe for concurrent use; work on one
// owner is serialized.
type Host struct {
	root  string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Open opens (creating if needed) the hosting store at root.
func Open(root string) (*Host, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create hosting store: %w", err)
	}
	return &Host{root: root, locks: map[string]*sync.Mutex{}}, nil
}

func (h *Host) lock(owner string) func() {
	h.mu.Lock()
	l, ok := h.locks[owner]
	if !ok {
		l = &sync.Mutex{}
		h.locks[owner] = l
	}
	h.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (h *Host) dir(owner string, parts ...string) string {
	return filepath.Join(append([]string{h.root, owner}, parts...)...)
}

func (h *Host) objPath(owner, kind, id string) string {
	return h.dir(owner, kind, id[:2], id)
}

func check(names ...string) error {
	for _, n := range names {
		if !validName.MatchString(n) {
			return fmt.Errorf("invalid name %q", n)
		}
	}
	return nil
}

// Get returns an owner's object, live or in trash.
func (h *Host) Get(owner, id string) ([]byte, error) {
	if check(owner, id) != nil {
		return nil, ErrNotFound
	}
	for _, kind := range []string{"objects", "trash"} {
		data, err := os.ReadFile(h.objPath(owner, kind, id))
		if err == nil {
			return data, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return nil, ErrNotFound
}

// Usage reports an owner's live bytes, trashed bytes and number of sets.
func (h *Host) Usage(owner string) (live, trash int64, sets int) {
	if check(owner) != nil {
		return 0, 0, 0
	}
	live, _ = dirSize(h.dir(owner, "objects"))
	trash, _ = dirSize(h.dir(owner, "trash"))
	entries, _ := os.ReadDir(h.dir(owner, "sets"))
	return live, trash, len(entries)
}

// Owners lists the owners with data on this host.
func (h *Host) Owners() []string {
	entries, _ := os.ReadDir(h.root)
	var out []string
	for _, e := range entries {
		if e.IsDir() && validName.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// Purge deletes everything held for an owner, trash included.
func (h *Host) Purge(owner string) error {
	if err := check(owner); err != nil {
		return err
	}
	defer h.lock(owner)()
	return os.RemoveAll(h.dir(owner))
}

// Session is one set replacement in progress; it holds the owner's lock until
// Close.
type Session struct {
	h      *Host
	owner  string
	setID  string
	unlock func()
}

// Begin starts replacing set setID of owner.
func (h *Host) Begin(owner, setID string) (*Session, error) {
	if err := check(owner, setID); err != nil {
		return nil, err
	}
	return &Session{h: h, owner: owner, setID: setID, unlock: h.lock(owner)}, nil
}

// Close releases the owner's lock.
func (s *Session) Close() { s.unlock() }

// Plan reports which of the entries the host lacks (indices into entries), and
// how many bytes the owner would hold once the set is committed: everything any
// of its sets would then list. Objects in trash count as present (Commit brings
// them back).
func (s *Session) Plan(entries []Entry) (missing []int, heldAfter int64, err error) {
	for _, e := range entries {
		if err := check(e.ID); err != nil {
			return nil, 0, err
		}
	}
	live, err := s.h.liveIDs(s.owner, s.setID)
	if err != nil {
		return nil, 0, err
	}
	for i, e := range entries {
		live[e.ID] = true
		if s.h.present(s.owner, e.ID) == "" {
			missing = append(missing, i)
			heldAfter += e.Size
		}
	}
	for id := range live {
		if p := s.h.present(s.owner, id); p != "" {
			if info, err := os.Stat(p); err == nil {
				heldAfter += info.Size()
			}
		}
	}
	return missing, heldAfter, nil
}

// Put stores one object of the set (written atomically).
func (s *Session) Put(id string, data []byte) error {
	if err := check(id); err != nil {
		return err
	}
	p := s.h.objPath(s.owner, "objects", id)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Commit makes ids the set's content (renewing its lease), brings listed objects
// back from trash, and moves objects no set lists any more to trash. It returns
// the owner's live bytes afterwards.
func (s *Session) Commit(ids []string) (int64, error) {
	for _, id := range ids {
		if err := check(id); err != nil {
			return 0, err
		}
		if s.h.present(s.owner, id) == s.h.objPath(s.owner, "trash", id) {
			if err := s.h.move(s.owner, id, "trash", "objects"); err != nil {
				return 0, err
			}
		}
	}
	data, err := json.Marshal(setFile{IDs: ids, UpdatedAt: time.Now().UTC()})
	if err != nil {
		return 0, err
	}
	p := s.h.dir(s.owner, "sets", s.setID+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return 0, err
	}
	if err := os.WriteFile(p+".tmp", data, 0o600); err != nil {
		return 0, err
	}
	if err := os.Rename(p+".tmp", p); err != nil {
		return 0, err
	}
	if _, err := s.h.collect(s.owner); err != nil {
		return 0, err
	}
	live, _, _ := s.h.Usage(s.owner)
	return live, nil
}

// ReapStats summarizes a Reap.
type ReapStats struct {
	ExpiredSets  int // sets dropped because their owner didn't renew them in time
	Trashed      int // objects moved to trash
	DeletedTrash int // trashed objects deleted after the grace period
	FreedBytes   int64
}

// Reap drops sets not renewed within lease, trashes objects no set lists, and
// deletes trash older than trashAge.
func (h *Host) Reap(lease, trashAge time.Duration) (ReapStats, error) {
	var st ReapStats
	now := time.Now()
	for _, owner := range h.Owners() {
		unlock := h.lock(owner)
		sets, _ := os.ReadDir(h.dir(owner, "sets"))
		for _, e := range sets {
			p := h.dir(owner, "sets", e.Name())
			sf, err := readSet(p)
			if err == nil && now.Sub(sf.UpdatedAt) > lease {
				if os.Remove(p) == nil {
					st.ExpiredSets++
				}
			}
		}
		n, err := h.collect(owner)
		st.Trashed += n
		if err != nil {
			unlock()
			return st, err
		}
		filepath.WalkDir(h.dir(owner, "trash"), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if info, err := d.Info(); err == nil && now.Sub(info.ModTime()) > trashAge {
				if os.Remove(p) == nil {
					st.DeletedTrash++
					st.FreedBytes += info.Size()
				}
			}
			return nil
		})
		unlock()
	}
	return st, nil
}

// liveIDs returns the ids listed by all of owner's sets except skipSet.
func (h *Host) liveIDs(owner, skipSet string) (map[string]bool, error) {
	live := map[string]bool{}
	sets, err := os.ReadDir(h.dir(owner, "sets"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range sets {
		if e.Name() == skipSet+".json" || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		sf, err := readSet(h.dir(owner, "sets", e.Name()))
		if err != nil {
			return nil, err
		}
		for _, id := range sf.IDs {
			live[id] = true
		}
	}
	return live, nil
}

// collect moves owner's objects that no set lists to trash.
func (h *Host) collect(owner string) (int, error) {
	live, err := h.liveIDs(owner, "")
	if err != nil {
		return 0, err
	}
	var dead []string
	filepath.WalkDir(h.dir(owner, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && validName.MatchString(d.Name()) && !live[d.Name()] {
			dead = append(dead, d.Name())
		}
		return nil
	})
	for _, id := range dead {
		if err := h.move(owner, id, "objects", "trash"); err != nil {
			return 0, err
		}
	}
	return len(dead), nil
}

// move renames an object between objects/ and trash/, stamping its mtime (the
// trash age starts when it is trashed).
func (h *Host) move(owner, id, from, to string) error {
	dst := h.objPath(owner, to, id)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Rename(h.objPath(owner, from, id), dst); err != nil {
		return err
	}
	now := time.Now()
	return os.Chtimes(dst, now, now)
}

// present returns the path where owner's object lives (live first, then trash),
// or "" if the host doesn't have it.
func (h *Host) present(owner, id string) string {
	for _, kind := range []string{"objects", "trash"} {
		p := h.objPath(owner, kind, id)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func readSet(p string) (setFile, error) {
	var sf setFile
	data, err := os.ReadFile(p)
	if err != nil {
		return sf, err
	}
	return sf, json.Unmarshal(data, &sf)
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}
