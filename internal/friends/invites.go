package friends

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Invite is a pending invitation created by `hidage friend invite`. Only a hash
// of its token is stored, so the file holds nothing that lets someone pair.
type Invite struct {
	Nickname string    `json:"nickname,omitempty"` // name for the friend who accepts (default: theirs)
	Give     int64     `json:"give"`
	Expires  time.Time `json:"expires"`
}

// Invites is the persisted set of pending invitations, keyed by token hash.
type Invites struct {
	path string
	mu   sync.Mutex
}

// OpenInvites returns the invitation store at path.
func OpenInvites(path string) *Invites { return &Invites{path: path} }

func tokenKey(token []byte) string {
	sum := sha256.Sum256(token)
	return hex.EncodeToString(sum[:])
}

func (iv *Invites) load() (map[string]Invite, error) {
	m := map[string]Invite{}
	raw, err := os.ReadFile(iv.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read invites: %w", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse invites: %w", err)
	}
	return m, nil
}

func (iv *Invites) save(m map[string]Invite) error {
	now := time.Now()
	for k, inv := range m {
		if now.After(inv.Expires) {
			delete(m, k)
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := iv.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, iv.path)
}

// Create records a new invitation valid for ttl and returns its token.
func (iv *Invites) Create(nickname string, give int64, ttl time.Duration) ([]byte, time.Time, error) {
	token := make([]byte, TokenSize)
	if _, err := rand.Read(token); err != nil {
		return nil, time.Time{}, err
	}
	iv.mu.Lock()
	defer iv.mu.Unlock()
	m, err := iv.load()
	if err != nil {
		return nil, time.Time{}, err
	}
	exp := time.Now().Add(ttl).Truncate(time.Second)
	m[tokenKey(token)] = Invite{Nickname: nickname, Give: give, Expires: exp}
	return token, exp, iv.save(m)
}

// Consume redeems a token: it returns the invitation and deletes it, or false
// if the token is unknown, already used or expired.
func (iv *Invites) Consume(token []byte) (Invite, bool) {
	iv.mu.Lock()
	defer iv.mu.Unlock()
	m, err := iv.load()
	if err != nil {
		return Invite{}, false
	}
	k := tokenKey(token)
	inv, ok := m[k]
	if !ok || time.Now().After(inv.Expires) {
		return Invite{}, false
	}
	delete(m, k)
	return inv, iv.save(m) == nil
}

// Pending reports whether any invitation is still open.
func (iv *Invites) Pending() bool {
	iv.mu.Lock()
	defer iv.mu.Unlock()
	m, err := iv.load()
	if err != nil {
		return false
	}
	now := time.Now()
	for _, inv := range m {
		if now.Before(inv.Expires) {
			return true
		}
	}
	return false
}
