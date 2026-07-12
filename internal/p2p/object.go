package p2p

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// ObjectProtocol is the request/response protocol for fetching one object by
// content address. Request: the id as a newline-terminated string. Response: a
// status byte (1 = found, 0 = missing), and if found an 8-byte big-endian length
// followed by the object bytes.
const ObjectProtocol = "/hidage/object/1.0.0"

// ErrObjectNotFound is returned when a peer does not have the requested object.
var ErrObjectNotFound = errors.New("peer does not have object")

func (n *Node) registerObjectHandler() {
	n.host.SetStreamHandler(ObjectProtocol, func(s network.Stream) {
		defer s.Close()
		br := bufio.NewReader(s)
		id, err := br.ReadString('\n')
		if err != nil {
			return
		}
		id = id[:len(id)-1] // strip newline

		if n.blobs == nil {
			s.Write([]byte{0}) // this node serves nothing (e.g. a publish/audit client)
			return
		}
		data, err := n.blobs.Get(id)
		if err != nil {
			s.Write([]byte{0}) // missing (or unreadable)
			return
		}
		var hdr [9]byte
		hdr[0] = 1
		binary.BigEndian.PutUint64(hdr[1:], uint64(len(data)))
		if _, err := s.Write(hdr[:]); err != nil {
			return
		}
		s.Write(data)
	})
}

// GetObject resolves an object by content address: it serves from the local
// blob store if present, otherwise discovers providers via the DHT and fetches
// from the first that answers.
func (n *Node) GetObject(ctx context.Context, id string) ([]byte, error) {
	if n.blobs != nil {
		if data, err := n.blobs.Get(id); err == nil {
			return data, nil
		}
	}
	if n.dht == nil {
		return nil, ErrObjectNotFound
	}
	provs, err := n.FindProviders(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, p := range provs {
		if p.ID == n.host.ID() {
			continue
		}
		if len(p.Addrs) > 0 {
			n.host.Connect(ctx, p)
		}
		if data, err := n.FetchObject(ctx, p.ID, id); err == nil {
			return data, nil
		}
	}
	return nil, ErrObjectNotFound
}

// FetchObject requests object id from peer p.
func (n *Node) FetchObject(ctx context.Context, p peer.ID, id string) ([]byte, error) {
	s, err := n.host.NewStream(ctx, p, ObjectProtocol)
	if err != nil {
		return nil, fmt.Errorf("open stream to %s: %w", p, err)
	}
	defer s.Close()

	if _, err := s.Write([]byte(id + "\n")); err != nil {
		return nil, err
	}
	if cw, ok := s.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}

	status := make([]byte, 1)
	if _, err := io.ReadFull(s, status); err != nil {
		return nil, fmt.Errorf("read status: %w", err)
	}
	if status[0] == 0 {
		return nil, ErrObjectNotFound
	}
	var lenBuf [8]byte
	if _, err := io.ReadFull(s, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}
	n64 := binary.BigEndian.Uint64(lenBuf[:])
	data := make([]byte, n64)
	if _, err := io.ReadFull(s, data); err != nil {
		return nil, fmt.Errorf("read object: %w", err)
	}
	return data, nil
}
