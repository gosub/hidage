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
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// AddrProtocol lets friends keep each other's addresses current. A node signs a
// record of its own addresses; any friend can ask it for that record, or ask it
// for the last record it holds for another of its friends. Records are signed by
// their subject and carry a sequence number, so a relayed record can't be forged
// and an older one can't displace a newer one.
//
// Request: the subject's hex public key + "\n". Response: a 4-byte big-endian
// length (0 = unknown) and the JSON record.
const AddrProtocol = "/hidage/addr/1.0.0"

const maxAddrRecord = 16 << 10

// AddrRecord is a node's signed statement of where it can be reached.
type AddrRecord struct {
	PubKey string   `json:"pubKey"` // hex Ed25519 public key of the subject
	Addrs  []string `json:"addrs"`  // multiaddrs without the /p2p suffix
	Seq    int64    `json:"seq"`    // unix nanoseconds when signed; newer wins
	Sig    []byte   `json:"sig"`
}

func addrPayload(r *AddrRecord) []byte {
	return []byte("hidage-addr\n" + r.PubKey + "\n" + fmt.Sprint(r.Seq) + "\n" + strings.Join(r.Addrs, "\n"))
}

// Verify checks the record's signature against its subject key.
func (r *AddrRecord) Verify() error {
	raw, err := hex.DecodeString(r.PubKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return errors.New("address record: bad public key")
	}
	if !ed25519.Verify(ed25519.PublicKey(raw), addrPayload(r), r.Sig) {
		return errors.New("address record: bad signature")
	}
	return nil
}

// SignedAddrRecord returns a fresh signed record of this node's addresses.
func (n *Node) SignedAddrRecord() *AddrRecord {
	r := &AddrRecord{PubKey: hex.EncodeToString(n.id.Public), Addrs: n.ListenAddrs(), Seq: time.Now().UnixNano()}
	r.Sig = ed25519.Sign(n.id.Private, addrPayload(r))
	return r
}

// EnableAddrService answers address requests: for this node itself with a fresh
// signed record, and for other subjects with whatever lookup returns (the
// stored, still-signed record of a friend, or nil).
func (n *Node) EnableAddrService(lookup func(subjectHex string) *AddrRecord) {
	self := hex.EncodeToString(n.id.Public)
	n.host.SetStreamHandler(AddrProtocol, func(s network.Stream) {
		defer s.Close()
		subject, err := bufio.NewReader(io.LimitReader(s, 256)).ReadString('\n')
		if err != nil {
			return
		}
		subject = strings.TrimSpace(subject)
		var rec *AddrRecord
		if subject == self {
			rec = n.SignedAddrRecord()
		} else if lookup != nil {
			rec = lookup(subject)
		}
		var data []byte
		if rec != nil {
			data, _ = json.Marshal(rec)
		}
		binary.Write(s, binary.BigEndian, uint32(len(data)))
		s.Write(data)
	})
}

// FetchAddrRecord asks peer p for the address record of subjectHex (p's own
// when subjectHex is p's key). It returns nil if p doesn't know the subject.
func (n *Node) FetchAddrRecord(ctx context.Context, p peer.ID, subjectHex string) (*AddrRecord, error) {
	s, err := n.host.NewStream(ctx, p, AddrProtocol)
	if err != nil {
		return nil, fmt.Errorf("open addr stream: %w", err)
	}
	defer s.Close()
	if _, err := s.Write([]byte(subjectHex + "\n")); err != nil {
		return nil, err
	}
	if cw, ok := s.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
	var length uint32
	if err := binary.Read(s, binary.BigEndian, &length); err != nil {
		return nil, err
	}
	if length == 0 {
		return nil, nil
	}
	if length > maxAddrRecord {
		return nil, errors.New("address record too large")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(s, data); err != nil {
		return nil, err
	}
	var rec AddrRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	if rec.PubKey != subjectHex {
		return nil, errors.New("address record is for a different peer")
	}
	if err := rec.Verify(); err != nil {
		return nil, err
	}
	return &rec, nil
}
