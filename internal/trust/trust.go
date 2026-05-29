// Package trust implements a node's local trust policy: an explicit allow-list
// (trusted keys) and block-list (blocked keys) of peer Ed25519 public keys. The
// policy is consulted before reputation when deciding whether to place shards on
// a peer or accept them from one:
//
//	Blocked  → never interact (overrides everything)
//	Trusted  → interact regardless of reputation (friend-to-friend)
//	Neutral  → defer to the reputation score (Phase 4)
//
// Trust is established out of band: you exchange public keys with someone you
// know, à la Syncthing device pairing. The policy file holds no secrets — just
// the public keys this node has an opinion about.
package trust

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

// Verdict is the policy decision for a peer.
type Verdict int

const (
	// Neutral means no explicit policy applies; callers fall back to reputation.
	Neutral Verdict = iota
	// Trusted means the peer is whitelisted; reputation is bypassed.
	Trusted
	// Blocked means the peer is blacklisted; never interact.
	Blocked
)

func (v Verdict) String() string {
	switch v {
	case Trusted:
		return "trusted"
	case Blocked:
		return "blocked"
	default:
		return "neutral"
	}
}

type fileFormat struct {
	Version int      `json:"version"`
	Trusted []string `json:"trusted"`
	Blocked []string `json:"blocked"`
}

// Policy is a node's trust state, persisted to a file.
type Policy struct {
	path    string
	trusted map[string]bool // hex-encoded pubkey
	blocked map[string]bool
}

// Load reads the policy at path, returning an empty policy if the file is absent.
func Load(path string) (*Policy, error) {
	p := &Policy{path: path, trusted: map[string]bool{}, blocked: map[string]bool{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read trust policy: %w", err)
	}
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse trust policy: %w", err)
	}
	for _, k := range f.Trusted {
		p.trusted[k] = true
	}
	for _, k := range f.Blocked {
		p.blocked[k] = true
	}
	return p, nil
}

// validate normalizes a hex public key and checks it is a 32-byte Ed25519 key.
func validate(hexKey string) (string, error) {
	b, err := hex.DecodeString(hexKey)
	if err != nil {
		return "", fmt.Errorf("invalid hex public key: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return "", fmt.Errorf("public key must be %d bytes, got %d", ed25519.PublicKeySize, len(b))
	}
	return hex.EncodeToString(b), nil // canonical lower-case form
}

// Verdict returns the policy decision for a public key.
func (p *Policy) Verdict(pub ed25519.PublicKey) Verdict {
	return p.verdictHex(hex.EncodeToString(pub))
}

func (p *Policy) verdictHex(key string) Verdict {
	if p.blocked[key] {
		return Blocked
	}
	if p.trusted[key] {
		return Trusted
	}
	return Neutral
}

// Trust adds a key to the allow-list (and clears any block on it).
func (p *Policy) Trust(hexKey string) error {
	key, err := validate(hexKey)
	if err != nil {
		return err
	}
	delete(p.blocked, key)
	p.trusted[key] = true
	return p.save()
}

// Block adds a key to the block-list (and clears any trust on it).
func (p *Policy) Block(hexKey string) error {
	key, err := validate(hexKey)
	if err != nil {
		return err
	}
	delete(p.trusted, key)
	p.blocked[key] = true
	return p.save()
}

// Remove drops a key from the allow-list.
func (p *Policy) Remove(hexKey string) error {
	key, err := validate(hexKey)
	if err != nil {
		return err
	}
	delete(p.trusted, key)
	return p.save()
}

// Unblock drops a key from the block-list.
func (p *Policy) Unblock(hexKey string) error {
	key, err := validate(hexKey)
	if err != nil {
		return err
	}
	delete(p.blocked, key)
	return p.save()
}

// List returns the trusted and blocked keys, each sorted.
func (p *Policy) List() (trusted, blocked []string) {
	return keys(p.trusted), keys(p.blocked)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (p *Policy) save() error {
	f := fileFormat{Version: 1, Trusted: keys(p.trusted), Blocked: keys(p.blocked)}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.path, data, 0o600)
}
