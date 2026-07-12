package p2p

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

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

// PutHead publishes a signed head pointer naming snapshotID as the owner's
// latest. It returns the exact record bytes it put, so the caller can also hand
// them to storage peers (which re-publish them, so discovery survives the owner
// going offline — see PushHead / RepublishHead).
func (n *Node) PutHead(ctx context.Context, id identity.Identity, snapshotID string) ([]byte, error) {
	rec := headRecord{SnapshotID: snapshotID, CreatedAt: time.Now().UnixNano()}
	rec.Sig = ed25519.Sign(id.Private, headPayload(snapshotID, rec.CreatedAt))
	val, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	return val, n.dht.PutValue(ctx, HeadKey(id.Public), val)
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

// HeadPushProtocol lets an owner hand a storage peer its signed head record, so
// the peer can keep re-publishing it to the DHT after the owner goes offline —
// making disaster recovery survive loss of the original machine, not just DHT
// record expiry while the owner is alive. The record is owner-signed and
// self-validating, so a peer re-serving it can neither forge nor alter it.
const HeadPushProtocol = "/hidage/head/1.0.0"

const maxHeadRecord = 4096 // a head record is a tiny signed JSON blob

// EnableHeadStore makes the node accept pushed head records. Each is validated
// (owner signature) before store is called with the owner's hex public key and
// the raw record bytes. A nil store disables acceptance.
func (n *Node) EnableHeadStore(store func(ownerHex string, val []byte) error) {
	n.host.SetStreamHandler(HeadPushProtocol, func(s network.Stream) {
		defer s.Close()
		br := bufio.NewReader(s)
		key, err := br.ReadString('\n')
		if err != nil {
			return
		}
		key = key[:len(key)-1]
		var length uint32
		if err := binary.Read(br, binary.BigEndian, &length); err != nil || length > maxHeadRecord {
			return
		}
		val := make([]byte, length)
		if _, err := io.ReadFull(br, val); err != nil {
			return
		}
		pub, err := pubFromKey(key)
		if err != nil {
			return
		}
		if err := (headValidator{}).Validate(key, val); err != nil {
			return // reject anything the owner didn't sign
		}
		if store != nil {
			_ = store(hex.EncodeToString(pub), val)
		}
		s.Write([]byte{1})
	})
}

// PushHead sends a signed head record for owner to peer p, which may persist and
// re-publish it. key must be HeadKey(owner) and val the bytes PutHead returned.
func (n *Node) PushHead(ctx context.Context, p peer.ID, key string, val []byte) error {
	s, err := n.host.NewStream(ctx, p, HeadPushProtocol)
	if err != nil {
		return fmt.Errorf("open head stream: %w", err)
	}
	defer s.Close()
	if _, err := s.Write([]byte(key + "\n")); err != nil {
		return err
	}
	if err := binary.Write(s, binary.BigEndian, uint32(len(val))); err != nil {
		return err
	}
	if _, err := s.Write(val); err != nil {
		return err
	}
	if cw, ok := s.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
	ack := make([]byte, 1)
	_, err = io.ReadFull(s, ack)
	return err
}

// RepublishHead re-puts a stored head record (for the owner with the given hex
// public key) to the DHT. The namespaced validator re-checks the signature, so a
// peer can only republish records an owner actually signed.
func (n *Node) RepublishHead(ctx context.Context, ownerHex string, val []byte) error {
	raw, err := hex.DecodeString(ownerHex)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return fmt.Errorf("bad owner key %q", ownerHex)
	}
	return n.dht.PutValue(ctx, HeadKey(ed25519.PublicKey(raw)), val)
}
