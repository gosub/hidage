package p2p

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"hidage/internal/identity"
)

// Head pointers let a peer find an owner's latest snapshot with no local state,
// IPNS-style: the DHT key is derived from the owner's public key and the value is
// a signed record naming the current snapshot. The headValidator (registered for
// the "hidage" namespace) verifies the signature on Put and Get and selects the
// newest record, so the DHT only accepts records an owner actually signed.

const headNamespace = "hidage"

type headRecord struct {
	SnapshotID string `json:"snapshotID"`
	CreatedAt  int64  `json:"createdAt"` // unix nanoseconds
	Sig        []byte `json:"sig"`
}

func headPayload(snapshotID string, createdAt int64) []byte {
	return []byte(snapshotID + "\n" + strconv.FormatInt(createdAt, 10))
}

// HeadKey is the DHT key for an owner's head pointer.
func HeadKey(pub ed25519.PublicKey) string {
	return "/" + headNamespace + "/" + hex.EncodeToString(pub)
}

// PutHead publishes a signed head pointer naming snapshotID as the owner's latest.
func (n *Node) PutHead(ctx context.Context, id identity.Identity, snapshotID string) error {
	rec := headRecord{SnapshotID: snapshotID, CreatedAt: time.Now().UnixNano()}
	rec.Sig = ed25519.Sign(id.Private, headPayload(snapshotID, rec.CreatedAt))
	val, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return n.dht.PutValue(ctx, HeadKey(id.Public), val)
}

// GetHead resolves an owner's latest snapshot id from the DHT.
func (n *Node) GetHead(ctx context.Context, pub ed25519.PublicKey) (string, error) {
	val, err := n.dht.GetValue(ctx, HeadKey(pub))
	if err != nil {
		return "", err
	}
	var rec headRecord
	if err := json.Unmarshal(val, &rec); err != nil {
		return "", err
	}
	return rec.SnapshotID, nil
}

// headValidator implements record.Validator for head pointers.
type headValidator struct{}

func pubFromKey(key string) (ed25519.PublicKey, error) {
	// key is "/hidage/<hexpubkey>"
	prefix := "/" + headNamespace + "/"
	if !strings.HasPrefix(key, prefix) {
		return nil, fmt.Errorf("not a head key: %q", key)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(key, prefix))
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("bad public key in head key")
	}
	return ed25519.PublicKey(raw), nil
}

func (headValidator) Validate(key string, value []byte) error {
	pub, err := pubFromKey(key)
	if err != nil {
		return err
	}
	var rec headRecord
	if err := json.Unmarshal(value, &rec); err != nil {
		return err
	}
	if !ed25519.Verify(pub, headPayload(rec.SnapshotID, rec.CreatedAt), rec.Sig) {
		return errors.New("head record signature invalid")
	}
	return nil
}

func (headValidator) Select(key string, values [][]byte) (int, error) {
	best, bestTime := -1, int64(-1)
	for i, v := range values {
		var rec headRecord
		if err := json.Unmarshal(v, &rec); err != nil {
			continue
		}
		if rec.CreatedAt > bestTime {
			best, bestTime = i, rec.CreatedAt
		}
	}
	if best < 0 {
		return 0, errors.New("no valid head records")
	}
	return best, nil
}
