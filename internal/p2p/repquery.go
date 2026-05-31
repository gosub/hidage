package p2p

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// ReputationProtocol lets a node ask a peer for *its* reputation of a third
// party — the basis of bounded, depth-1 transitive trust. The answer is the
// peer's own local opinion; it's attributable (the stream runs over libp2p's
// authenticated channel, so we know which peer replied) but self-reported, so
// callers weigh it by how much they trust the responder. Request: target pubkey
// hex + "\n". Response: two big-endian float64s — score and the evidence (sample
// mass) behind it.
const ReputationProtocol = "/hidage/reputation/1.0.0"

const repQueryDeadline = 5 * time.Second

// EnableReputationService answers reputation queries from lookup (a node's own
// reputation store). Without it, the node returns "no opinion" (zero evidence).
func (n *Node) EnableReputationService(lookup func(pubHex string) (score, samples float64)) {
	n.repLookup = lookup
	n.host.SetStreamHandler(ReputationProtocol, func(s network.Stream) {
		defer s.Close()
		br := bufio.NewReader(s)
		target, err := br.ReadString('\n')
		if err != nil {
			return
		}
		target = target[:len(target)-1]
		var score, samples float64
		if n.repLookup != nil {
			score, samples = n.repLookup(target)
		}
		var buf [16]byte
		binary.BigEndian.PutUint64(buf[0:8], math.Float64bits(score))
		binary.BigEndian.PutUint64(buf[8:16], math.Float64bits(samples))
		s.Write(buf[:])
	})
}

// AskReputation asks peer p for its opinion (score, evidence) of targetHex.
func (n *Node) AskReputation(ctx context.Context, p peer.ID, targetHex string) (score, samples float64, err error) {
	s, err := n.host.NewStream(ctx, p, ReputationProtocol)
	if err != nil {
		return 0, 0, fmt.Errorf("open reputation stream: %w", err)
	}
	defer s.Close()
	if _, err := s.Write([]byte(targetHex + "\n")); err != nil {
		return 0, 0, err
	}
	if cw, ok := s.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
	s.SetReadDeadline(time.Now().Add(repQueryDeadline))

	var buf [16]byte
	if _, err := io.ReadFull(s, buf[:]); err != nil {
		return 0, 0, err
	}
	score = math.Float64frombits(binary.BigEndian.Uint64(buf[0:8]))
	samples = math.Float64frombits(binary.BigEndian.Uint64(buf[8:16]))
	return score, samples, nil
}
