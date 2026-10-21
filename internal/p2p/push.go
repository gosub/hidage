package p2p

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"hidage/internal/hosting"
)

// PushProtocol replaces one of the sender's hosted sets on a friend (see
// internal/hosting), transferring only the objects the host lacks.
//
//	sender → host: set id + "\n", uint32 count, then per object: uint8 id length,
//	               id, uint64 size
//	host → sender: status byte (1 = go ahead, 0 = not a friend, 2 = over quota),
//	               uint64 bytes held after the push, uint64 quota; if 1, uint32
//	               count of missing objects and their uint32 indices
//	sender → host: the missing objects' bytes, in that order (sizes as declared)
//	host → sender: status byte (1 = committed), uint64 bytes now held
const PushProtocol = "/hidage/push/2.0.0"

// MaxObjectSize caps a single pushed or fetched object. Legitimate objects are
// raw chunks (FastCDC max 4 MiB) or erasure-coded shards (~256 KiB); this bound
// leaves headroom while preventing a peer from making a node allocate gigabytes
// from a single attacker-supplied length.
const MaxObjectSize = 16 << 20

// maxIDLen bounds an object or set id on the wire.
const maxIDLen = 128

// maxSetEntries bounds how many objects one set may list.
const maxSetEntries = 1 << 20

const (
	pushGo        = 1
	pushNotFriend = 0
	pushOverQuota = 2
)

// ErrNotFriend means the host refused because the sender is not its friend.
var ErrNotFriend = errors.New("refused: you are not on their friend list")

// QuotaError means the push would exceed the space the host gives the sender.
type QuotaError struct {
	Need, Give int64
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("over quota: this backup needs %d bytes there, they give you %d", e.Need, e.Give)
}

// PushResult describes a completed push.
type PushResult struct {
	Objects   int   // objects in the set
	Sent      int   // objects actually transferred (the rest were already there)
	SentBytes int64 // bytes transferred
	Held      int64 // bytes the host now holds for the sender, all sets included
	Give      int64 // the sender's quota on the host
}

// EnableHosting makes the node keep friends' sets in h. quota returns the bytes
// an owner (hex public key) may hold here, or 0 to refuse it; onPushed, if not
// nil, is called after each committed push with the owner's bytes now held.
// Object and audit requests are then answered from the requester's own data.
func (n *Node) EnableHosting(h *hosting.Host, quota func(ownerHex string) int64, onPushed func(ownerHex string, held int64)) {
	n.hosted = h
	n.host.SetStreamHandler(PushProtocol, func(s network.Stream) {
		defer s.Close()
		owner, err := PeerPubKeyHex(s.Conn().RemotePeer())
		if err != nil {
			return
		}
		if err := n.handlePush(s, owner, quota(owner)); err == nil && onPushed != nil {
			live, _, _ := h.Usage(owner)
			onPushed(owner, live)
		}
	})
}

func (n *Node) handlePush(s network.Stream, owner string, give int64) error {
	br := bufio.NewReader(s)
	setID, err := readLine(br)
	if err != nil {
		return err
	}
	var count uint32
	if err := binary.Read(br, binary.BigEndian, &count); err != nil {
		return err
	}
	if count > maxSetEntries {
		return fmt.Errorf("set too large: %d entries", count)
	}
	entries := make([]hosting.Entry, count)
	for i := range entries {
		var l uint8
		if err := binary.Read(br, binary.BigEndian, &l); err != nil {
			return err
		}
		id := make([]byte, l)
		if _, err := io.ReadFull(br, id); err != nil {
			return err
		}
		var size uint64
		if err := binary.Read(br, binary.BigEndian, &size); err != nil {
			return err
		}
		if size > MaxObjectSize {
			return fmt.Errorf("object too large: %d", size)
		}
		entries[i] = hosting.Entry{ID: string(id), Size: int64(size)}
	}

	reply := func(status byte, held int64) error {
		var hdr [17]byte
		hdr[0] = status
		binary.BigEndian.PutUint64(hdr[1:9], uint64(held))
		binary.BigEndian.PutUint64(hdr[9:], uint64(give))
		_, err := s.Write(hdr[:])
		return err
	}
	if give <= 0 {
		reply(pushNotFriend, 0)
		return ErrNotFriend
	}
	sess, err := n.hosted.Begin(owner, setID)
	if err != nil {
		return err
	}
	defer sess.Close()
	missing, heldAfter, err := sess.Plan(entries)
	if err != nil {
		return err
	}
	if heldAfter > give {
		reply(pushOverQuota, heldAfter)
		return &QuotaError{Need: heldAfter, Give: give}
	}
	bw := bufio.NewWriter(s)
	if err := reply(pushGo, heldAfter); err != nil {
		return err
	}
	binary.Write(bw, binary.BigEndian, uint32(len(missing)))
	for _, i := range missing {
		binary.Write(bw, binary.BigEndian, uint32(i))
	}
	if err := bw.Flush(); err != nil {
		return err
	}

	for _, i := range missing {
		data := make([]byte, entries[i].Size)
		if _, err := io.ReadFull(br, data); err != nil {
			return err
		}
		if err := sess.Put(entries[i].ID, data); err != nil {
			return err
		}
	}
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	held, err := sess.Commit(ids)
	if err != nil {
		return err
	}
	var done [9]byte
	done[0] = 1
	binary.BigEndian.PutUint64(done[1:], uint64(held))
	_, err = s.Write(done[:])
	return err
}

// PushSet makes objs the content of the sender's set setID on peer p,
// transferring only what p lacks.
func (n *Node) PushSet(ctx context.Context, p peer.ID, setID string, objs map[string][]byte) (*PushResult, error) {
	entries := make([]hosting.Entry, 0, len(objs))
	for id, data := range objs {
		entries = append(entries, hosting.Entry{ID: id, Size: int64(len(data))})
	}
	return n.PushEntries(ctx, p, setID, entries, func(id string) ([]byte, error) { return objs[id], nil })
}

// PushEntries is PushSet for sets described by id and size: get is called only
// for the objects p lacks, so data that is expensive to produce (e.g. shards of
// an older release, rebuilt on demand) is produced only when needed.
func (n *Node) PushEntries(ctx context.Context, p peer.ID, setID string, entries []hosting.Entry, get func(id string) ([]byte, error)) (*PushResult, error) {
	s, err := n.host.NewStream(ctx, p, PushProtocol)
	if err != nil {
		return nil, fmt.Errorf("open push stream: %w", err)
	}
	defer s.Close()

	entries = append([]hosting.Entry(nil), entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	for _, e := range entries {
		if len(e.ID) > maxIDLen {
			return nil, fmt.Errorf("object id too long: %q", e.ID)
		}
	}

	bw := bufio.NewWriter(s)
	bw.WriteString(setID + "\n")
	binary.Write(bw, binary.BigEndian, uint32(len(entries)))
	for _, e := range entries {
		bw.WriteByte(uint8(len(e.ID)))
		bw.WriteString(e.ID)
		binary.Write(bw, binary.BigEndian, uint64(e.Size))
	}
	if err := bw.Flush(); err != nil {
		return nil, err
	}

	br := bufio.NewReader(s)
	var hdr [17]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return nil, fmt.Errorf("read push reply: %w", err)
	}
	res := &PushResult{Objects: len(entries), Held: int64(binary.BigEndian.Uint64(hdr[1:9])), Give: int64(binary.BigEndian.Uint64(hdr[9:]))}
	switch hdr[0] {
	case pushNotFriend:
		return nil, ErrNotFriend
	case pushOverQuota:
		return nil, &QuotaError{Need: res.Held, Give: res.Give}
	case pushGo:
	default:
		return nil, fmt.Errorf("unexpected push status %d", hdr[0])
	}
	var nMissing uint32
	if err := binary.Read(br, binary.BigEndian, &nMissing); err != nil {
		return nil, err
	}
	if nMissing > uint32(len(entries)) {
		return nil, errors.New("host asked for more objects than the set has")
	}
	missing := make([]uint32, nMissing)
	if err := binary.Read(br, binary.BigEndian, missing); err != nil {
		return nil, err
	}
	for _, i := range missing {
		if int(i) >= len(entries) {
			return nil, errors.New("host asked for an object outside the set")
		}
		data, err := get(entries[i].ID)
		if err != nil {
			return nil, fmt.Errorf("object %s: %w", entries[i].ID, err)
		}
		if int64(len(data)) != entries[i].Size {
			return nil, fmt.Errorf("object %s: %d bytes, declared %d", entries[i].ID, len(data), entries[i].Size)
		}
		if _, err := bw.Write(data); err != nil {
			return nil, err
		}
		res.Sent++
		res.SentBytes += int64(len(data))
	}
	if err := bw.Flush(); err != nil {
		return nil, err
	}
	var done [9]byte
	if _, err := io.ReadFull(br, done[:]); err != nil || done[0] != 1 {
		return nil, fmt.Errorf("host did not commit the set: %v", err)
	}
	res.Held = int64(binary.BigEndian.Uint64(done[1:]))
	return res, nil
}

func readLine(br *bufio.Reader) (string, error) {
	var b []byte
	for len(b) <= maxIDLen {
		c, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if c == '\n' {
			return string(b), nil
		}
		b = append(b, c)
	}
	return "", errors.New("line too long")
}
