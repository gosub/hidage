package p2p

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// AuditProtocol is a proof-of-storage challenge. The challenger (who has the
// original) sends an object id and a fresh random nonce; the holder must return
// SHA-256(nonce || objectBytes) — only computable if it still has the bytes —
// signed by its identity. The signature makes the response a verifiable receipt:
// evidence, attributable to the holder, that it answered a specific challenge.
//
// Request:  id "\n", then a 32-byte nonce.
// Response: status byte (1 = held, 0 = missing); if held, 32-byte proof then a
// 64-byte Ed25519 signature over id||nonce||proof.
const AuditProtocol = "/hidage/audit/1.0.0"

const nonceSize = 32

// auditDeadline bounds how long a holder may take to answer a challenge. An
// honest holder reads the object from local disk and hashes it (fast); a lazy
// one trying to fetch it from another peer first should exceed this. It's a soft
// defense — tiny objects can still be outsourced within the window.
const auditDeadline = 5 * time.Second

// Receipt is the challenger's record of one audit.
type Receipt struct {
	Peer      peer.ID
	ObjectID  string
	Nonce     []byte
	Proof     []byte
	Sig       []byte
	OK        bool
	CreatedAt time.Time
}

func auditAttestation(id string, nonce, proof []byte) []byte {
	b := make([]byte, 0, len(id)+len(nonce)+len(proof))
	b = append(b, id...)
	b = append(b, nonce...)
	b = append(b, proof...)
	return b
}

func (n *Node) registerAuditHandler() {
	n.host.SetStreamHandler(AuditProtocol, func(s network.Stream) {
		defer s.Close()
		br := bufio.NewReader(s)
		id, err := br.ReadString('\n')
		if err != nil {
			return
		}
		id = id[:len(id)-1]
		nonce := make([]byte, nonceSize)
		if _, err := io.ReadFull(br, nonce); err != nil {
			return
		}

		data, err := n.lookup(s.Conn().RemotePeer(), id)
		if err != nil {
			s.Write([]byte{0})
			return
		}
		sum := sha256.Sum256(append(append([]byte{}, nonce...), data...))
		sig := ed25519.Sign(n.id.Private, auditAttestation(id, nonce, sum[:]))

		out := make([]byte, 0, 1+len(sum)+len(sig))
		out = append(out, 1)
		out = append(out, sum[:]...)
		out = append(out, sig...)
		s.Write(out)
	})
}

// Audit challenges peer p to prove it still holds objectID, whose true contents
// the caller supplies as expected. It returns a Receipt; Receipt.OK reports
// whether the proof and signature verified.
func (n *Node) Audit(ctx context.Context, p peer.ID, objectID string, expected []byte) (*Receipt, error) {
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	s, err := n.host.NewStream(ctx, p, AuditProtocol)
	if err != nil {
		return nil, fmt.Errorf("open audit stream: %w", err)
	}
	defer s.Close()

	if _, err := s.Write(append([]byte(objectID+"\n"), nonce...)); err != nil {
		return nil, err
	}
	if cw, ok := s.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
	s.SetReadDeadline(time.Now().Add(auditDeadline)) // bound the response time

	rec := &Receipt{Peer: p, ObjectID: objectID, Nonce: nonce, CreatedAt: time.Now().UTC()}

	status := make([]byte, 1)
	if _, err := io.ReadFull(s, status); err != nil {
		return nil, fmt.Errorf("read audit status: %w", err)
	}
	if status[0] == 0 {
		return rec, nil // holder reports it does not have the object; OK stays false
	}

	proof := make([]byte, sha256.Size)
	sig := make([]byte, ed25519.SignatureSize)
	if _, err := io.ReadFull(s, proof); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(s, sig); err != nil {
		return nil, err
	}
	rec.Proof, rec.Sig = proof, sig

	pub, err := p.ExtractPublicKey()
	if err != nil {
		return rec, nil
	}
	rawPub, err := pub.Raw()
	if err != nil {
		return rec, nil
	}
	expectedProof := sha256.Sum256(append(append([]byte{}, nonce...), expected...))
	sigOK := ed25519.Verify(ed25519.PublicKey(rawPub), auditAttestation(objectID, nonce, proof), sig)
	rec.OK = sigOK && string(proof) == string(expectedProof[:])
	return rec, nil
}
