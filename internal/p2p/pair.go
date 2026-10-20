package p2p

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// PairProtocol turns an invite into a mutual friendship. The invitee presents
// the invite's token and its own contact card; the inviter's node checks and
// consumes the token, adds the invitee, and answers with its current card.
//
//	invitee → inviter: 16-byte token, uint16 length + contact card
//	inviter → invitee: status byte (1 = paired, 0 = refused), uint16 length + contact card
const PairProtocol = "/hidage/pair/1.0.0"

const (
	pairTokenSize = 16
	maxCardLen    = 8 << 10
)

// ErrPairRefused means the inviter's node rejected the token (unknown, used or
// expired).
var ErrPairRefused = errors.New("invite refused (already used or expired?)")

// EnablePairing answers pairing requests: accept is called with the requester,
// the token and its card, and returns this node's card to send back, or false
// to refuse.
func (n *Node) EnablePairing(accept func(from peer.ID, token []byte, card string) (string, bool)) {
	n.host.SetStreamHandler(PairProtocol, func(s network.Stream) {
		defer s.Close()
		s.SetReadDeadline(time.Now().Add(30 * time.Second))
		token := make([]byte, pairTokenSize)
		if _, err := io.ReadFull(s, token); err != nil {
			return
		}
		card, err := readCard(s)
		if err != nil {
			return
		}
		reply, ok := accept(s.Conn().RemotePeer(), token, card)
		if !ok {
			s.Write([]byte{0})
			return
		}
		s.Write([]byte{1})
		writeCard(s, reply)
	})
}

// Pair presents an invite token and this node's card to peer p, returning the
// inviter's card on success.
func (n *Node) Pair(ctx context.Context, p peer.ID, token []byte, card string) (string, error) {
	if len(token) != pairTokenSize {
		return "", errors.New("bad invite token")
	}
	s, err := n.host.NewStream(ctx, p, PairProtocol)
	if err != nil {
		return "", fmt.Errorf("open pair stream: %w", err)
	}
	defer s.Close()
	if _, err := s.Write(token); err != nil {
		return "", err
	}
	if err := writeCard(s, card); err != nil {
		return "", err
	}
	status := make([]byte, 1)
	if _, err := io.ReadFull(s, status); err != nil {
		return "", fmt.Errorf("read pair reply: %w", err)
	}
	if status[0] != 1 {
		return "", ErrPairRefused
	}
	return readCard(s)
}

func writeCard(w io.Writer, card string) error {
	if len(card) > maxCardLen {
		return errors.New("contact card too long")
	}
	if err := binary.Write(w, binary.BigEndian, uint16(len(card))); err != nil {
		return err
	}
	_, err := io.WriteString(w, card)
	return err
}

func readCard(r io.Reader) (string, error) {
	var l uint16
	if err := binary.Read(r, binary.BigEndian, &l); err != nil {
		return "", err
	}
	if int(l) > maxCardLen {
		return "", errors.New("contact card too long")
	}
	b := make([]byte, l)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}
