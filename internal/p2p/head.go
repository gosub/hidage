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

// Head pointers let an owner find its latest account manifest with no local
// state: a small record naming the manifest's content address, signed by the
// owner. The owner hands the record to every friend holding its data
// (HeadPushProtocol); a recovering owner asks its friends for it back
// (HeadGetProtocol) and keeps the newest valid one. Friends can withhold a record
// or serve an older one, but cannot forge or alter it.

const headNamespace = "hidage"

type headRecord struct {
	SnapshotID string `json:"snapshotID"` // the account manifest's content address
	CreatedAt  int64  `json:"createdAt"`  // unix nanoseconds; newest wins
	Sig        []byte `json:"sig"`
}

func headPayload(snapshotID string, createdAt int64) []byte {
	return []byte(snapshotID + "\n" + strconv.FormatInt(createdAt, 10))
}

// HeadKey names an owner's head record on the wire.
func HeadKey(pub ed25519.PublicKey) string {
	return "/" + headNamespace + "/" + hex.EncodeToString(pub)
}

// SignHead returns a signed head record naming manifestID as the owner's latest.
func SignHead(id identity.Identity, manifestID string) ([]byte, error) {
	rec := headRecord{SnapshotID: manifestID, CreatedAt: time.Now().UnixNano()}
	rec.Sig = ed25519.Sign(id.Private, headPayload(manifestID, rec.CreatedAt))
	return json.Marshal(rec)
}

func pubFromKey(key string) (ed25519.PublicKey, error) {
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

// verifyHead parses a head record and checks the owner's signature.
func verifyHead(owner ed25519.PublicKey, val []byte) (headRecord, error) {
	var rec headRecord
	if err := json.Unmarshal(val, &rec); err != nil {
		return rec, err
	}
	if !ed25519.Verify(owner, headPayload(rec.SnapshotID, rec.CreatedAt), rec.Sig) {
		return rec, errors.New("head record signature invalid")
	}
	return rec, nil
}

// HeadPushProtocol lets an owner hand a friend its signed head record, so the
// friend can return it during recovery, after the owner's machine is gone.
const HeadPushProtocol = "/hidage/head/1.0.0"

// HeadGetProtocol asks a friend for the head record it holds for the requester.
// Request: nothing (the requester is the owner, authenticated by the
// connection). Response: a 4-byte big-endian length (0 = none) and the record.
const HeadGetProtocol = "/hidage/head-get/1.0.0"

const maxHeadRecord = 4096 // a head record is a tiny signed JSON blob

// EnableHeadStore makes the node keep owners' head records: pushed records are
// verified and passed to put (with the owner's hex public key); an owner asking
// for its own record gets what get returns.
func (n *Node) EnableHeadStore(put func(ownerHex string, val []byte) error, get func(ownerHex string) ([]byte, error)) {
	n.host.SetStreamHandler(HeadPushProtocol, func(s network.Stream) {
		defer s.Close()
		if !n.isFriend(s.Conn().RemotePeer()) {
			return
		}
		br := bufio.NewReader(s)
		key, err := br.ReadString('\n')
		if err != nil {
			return
		}
		var length uint32
		if err := binary.Read(br, binary.BigEndian, &length); err != nil || length > maxHeadRecord {
			return
		}
		val := make([]byte, length)
		if _, err := io.ReadFull(br, val); err != nil {
			return
		}
		pub, err := pubFromKey(key[:len(key)-1])
		if err != nil {
			return
		}
		// Only the owner may set its head, and only with a record it signed.
		if sender, err := PeerPubKeyHex(s.Conn().RemotePeer()); err != nil || sender != hex.EncodeToString(pub) {
			return
		}
		if _, err := verifyHead(pub, val); err != nil {
			return
		}
		if put != nil && put(hex.EncodeToString(pub), val) == nil {
			s.Write([]byte{1})
		}
	})
	n.host.SetStreamHandler(HeadGetProtocol, func(s network.Stream) {
		defer s.Close()
		owner, err := PeerPubKeyHex(s.Conn().RemotePeer())
		var val []byte
		if err == nil && get != nil && n.isFriend(s.Conn().RemotePeer()) {
			val, _ = get(owner)
		}
		binary.Write(s, binary.BigEndian, uint32(len(val)))
		s.Write(val)
	})
}

// PushHead sends this node's signed head record (from SignHead) to peer p.
func (n *Node) PushHead(ctx context.Context, p peer.ID, val []byte) error {
	s, err := n.host.NewStream(ctx, p, HeadPushProtocol)
	if err != nil {
		return fmt.Errorf("open head stream: %w", err)
	}
	defer s.Close()
	if _, err := s.Write([]byte(HeadKey(n.id.Public) + "\n")); err != nil {
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
	if _, err := io.ReadFull(s, ack); err != nil {
		return fmt.Errorf("peer did not store the head record: %w", err)
	}
	return nil
}

// FetchHead asks peer p for the head record it holds for this node, returning
// the manifest id it names and its timestamp. It returns "" if p holds none.
func (n *Node) FetchHead(ctx context.Context, p peer.ID) (string, int64, error) {
	s, err := n.host.NewStream(ctx, p, HeadGetProtocol)
	if err != nil {
		return "", 0, fmt.Errorf("open head-get stream: %w", err)
	}
	defer s.Close()
	if cw, ok := s.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
	var length uint32
	if err := binary.Read(s, binary.BigEndian, &length); err != nil {
		return "", 0, err
	}
	if length == 0 {
		return "", 0, nil
	}
	if length > maxHeadRecord {
		return "", 0, errors.New("head record too large")
	}
	val := make([]byte, length)
	if _, err := io.ReadFull(s, val); err != nil {
		return "", 0, err
	}
	rec, err := verifyHead(n.id.Public, val)
	if err != nil {
		return "", 0, err
	}
	return rec.SnapshotID, rec.CreatedAt, nil
}

// ResolveHead asks every peer for this node's head record and returns the
// manifest id named by the newest valid one ("" if no peer holds one).
func (n *Node) ResolveHead(ctx context.Context, peers []peer.ID) string {
	best, bestAt := "", int64(-1)
	for _, p := range peers {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		id, at, err := n.FetchHead(c, p)
		cancel()
		if err == nil && id != "" && at > bestAt {
			best, bestAt = id, at
		}
	}
	return best
}
