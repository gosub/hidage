package p2p

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// PushProtocol replicates objects to a peer that agrees to store them. Wire:
// the receiver first sends a status byte (1 = accepted, 0 = refused) based on its
// trust policy; if accepted, the sender writes a uint32 count followed by, per
// object, uint32 idLen, id, uint64 dataLen, data; the receiver replies with a
// uint32 count of objects stored.
const PushProtocol = "/hidage/push/1.0.0"

// EnablePush makes the node accept pushed objects into sink. accept is consulted
// with the sender's raw Ed25519 public key and must return true to store; a nil
// accept stores from everyone. onStored, if non-nil, is called after a push with
// the sender's key and the number of bytes stored (for space accounting). Stored
// objects are re-announced to the DHT.
func (n *Node) EnablePush(sink Sink, accept func(senderPub []byte) bool, onStored func(senderPub []byte, bytes int64)) {
	n.sink = sink
	n.acceptPush = accept
	n.onStored = onStored
	n.host.SetStreamHandler(PushProtocol, n.handlePush)
}

func (n *Node) handlePush(s network.Stream) {
	defer s.Close()

	var rawPub []byte
	if pk := s.Conn().RemotePublicKey(); pk != nil {
		rawPub, _ = pk.Raw()
	}
	accepted := n.acceptPush == nil || (rawPub != nil && n.acceptPush(rawPub))
	if !accepted {
		s.Write([]byte{0})
		return
	}
	if _, err := s.Write([]byte{1}); err != nil {
		return
	}

	br := bufio.NewReader(s)
	var count uint32
	if err := binary.Read(br, binary.BigEndian, &count); err != nil {
		return
	}
	var stored uint32
	var storedBytes int64
	for i := uint32(0); i < count; i++ {
		id, data, err := readObject(br)
		if err != nil {
			break
		}
		if err := n.sink.Put(id, data); err != nil {
			break
		}
		n.provideStored(id)
		stored++
		storedBytes += int64(len(data))
	}
	binary.Write(s, binary.BigEndian, stored)
	if n.onStored != nil && rawPub != nil {
		n.onStored(rawPub, storedBytes)
	}
}

// provideStored best-effort announces a stored object to the DHT.
func (n *Node) provideStored(id string) {
	if n.dht == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = n.Provide(ctx, id)
}

// PushObjects sends objects to peer p, which stores them if its policy allows.
// Returns the number the peer reports stored.
func (n *Node) PushObjects(ctx context.Context, p peer.ID, objs map[string][]byte) (int, error) {
	s, err := n.host.NewStream(ctx, p, PushProtocol)
	if err != nil {
		return 0, fmt.Errorf("open push stream: %w", err)
	}
	defer s.Close()

	status := make([]byte, 1)
	if _, err := io.ReadFull(s, status); err != nil {
		return 0, fmt.Errorf("read accept status: %w", err)
	}
	if status[0] == 0 {
		return 0, fmt.Errorf("peer %s refused (not trusted?)", p)
	}

	bw := bufio.NewWriter(s)
	if err := binary.Write(bw, binary.BigEndian, uint32(len(objs))); err != nil {
		return 0, err
	}
	for id, data := range objs {
		if err := writeObject(bw, id, data); err != nil {
			return 0, err
		}
	}
	if err := bw.Flush(); err != nil {
		return 0, err
	}

	var stored uint32
	if err := binary.Read(s, binary.BigEndian, &stored); err != nil {
		return 0, fmt.Errorf("read stored count: %w", err)
	}
	return int(stored), nil
}

func writeObject(w io.Writer, id string, data []byte) error {
	if err := binary.Write(w, binary.BigEndian, uint32(len(id))); err != nil {
		return err
	}
	if _, err := io.WriteString(w, id); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint64(len(data))); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func readObject(r io.Reader) (string, []byte, error) {
	var idLen uint32
	if err := binary.Read(r, binary.BigEndian, &idLen); err != nil {
		return "", nil, err
	}
	idBuf := make([]byte, idLen)
	if _, err := io.ReadFull(r, idBuf); err != nil {
		return "", nil, err
	}
	var dataLen uint64
	if err := binary.Read(r, binary.BigEndian, &dataLen); err != nil {
		return "", nil, err
	}
	data := make([]byte, dataLen)
	if _, err := io.ReadFull(r, data); err != nil {
		return "", nil, err
	}
	return string(idBuf), data, nil
}
