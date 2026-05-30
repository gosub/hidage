// Package pack adds erasure-coded redundancy to distribution. A snapshot's
// objects are grouped into packs; each pack is erasure-coded (internal/shard)
// into M shards of which any K reconstruct it. Shards are spread across peers,
// so the data survives losing a minority of them — at a fraction of the cost of
// storing full replicas. A Release descriptor records, per pack, the coding
// parameters and the shard ids, so a recovering node can gather K shards from
// whoever still has them and rebuild the objects.
package pack

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"sort"

	"hidage/internal/shard"
)

// MaxPackSize bounds how much object data goes into one pack before erasure
// coding (so each pack splits into reasonably sized symbols).
const MaxPackSize = 8 << 20 // 8 MiB

// ShardRef names one shard and its index within the pack's shard set.
type ShardRef struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
}

// PackDesc describes one erasure-coded pack.
type PackDesc struct {
	Params shard.Params `json:"params"`
	Shards []ShardRef   `json:"shards"`
}

// Release is the distribution descriptor for a snapshot: the snapshot id plus
// the packs its objects were coded into. It is itself published as an object and
// pointed to by the head pointer.
type Release struct {
	SnapshotID string     `json:"snapshotID"`
	Packs      []PackDesc `json:"packs"`
}

// Build groups objects into packs, erasure-codes each, and returns the Release
// plus every shard blob (id → bytes) to distribute. redundancy is the ratio of
// shards to the minimum needed (e.g. 2.0 emits ~2× the shards required).
func Build(objects map[string][]byte, redundancy float64, snapshotID string) (*Release, map[string][]byte, error) {
	rel := &Release{SnapshotID: snapshotID}
	shardBlobs := map[string][]byte{}

	for _, group := range groupBySize(objects, MaxPackSize) {
		packBytes := encodePack(group, objects)
		params, shards, err := shard.Encode(packBytes, redundancy)
		if err != nil {
			return nil, nil, err
		}
		desc := PackDesc{Params: params}
		for _, s := range shards {
			id := hashID(s.Data)
			shardBlobs[id] = s.Data
			desc.Shards = append(desc.Shards, ShardRef{ID: id, Index: s.Index})
		}
		rel.Packs = append(rel.Packs, desc)
	}
	return rel, shardBlobs, nil
}

// Reassemble rebuilds the object map from a Release, fetching shards via get
// (which returns an error for shards that are unavailable). It needs only K
// shards per pack, so it tolerates missing shards/peers.
func Reassemble(rel *Release, get func(id string) ([]byte, error)) (map[string][]byte, error) {
	objects := map[string][]byte{}
	for i, desc := range rel.Packs {
		var have []shard.Shard
		for _, ref := range desc.Shards {
			data, err := get(ref.ID)
			if err != nil {
				continue // shard unavailable; that's what the redundancy is for
			}
			if hashID(data) != ref.ID {
				continue // a peer served the wrong bytes for this id; ignore it
			}
			have = append(have, shard.Shard{Index: ref.Index, Data: data})
		}
		packBytes, err := shard.Decode(desc.Params, have)
		if err != nil {
			return nil, fmt.Errorf("pack %d: %w", i, err)
		}
		group, err := decodePack(packBytes)
		if err != nil {
			return nil, fmt.Errorf("pack %d: %w", i, err)
		}
		for id, data := range group {
			objects[id] = data
		}
	}
	return objects, nil
}

// groupBySize greedily bins object ids into groups no larger than max bytes.
// Ids are sorted first so Build is deterministic: the same objects always yield
// the same packs and therefore the same shard ids, which lets a republish renew
// existing shards (and their storage leases) instead of producing new ones.
func groupBySize(objects map[string][]byte, max int) [][]string {
	ids := make([]string, 0, len(objects))
	for id := range objects {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var groups [][]string
	var cur []string
	var size int
	for _, id := range ids {
		data := objects[id]
		if size > 0 && size+len(data) > max {
			groups = append(groups, cur)
			cur, size = nil, 0
		}
		cur = append(cur, id)
		size += len(data)
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

// encodePack serializes a group of objects: uint32 count, then per object
// uint32 idLen, id, uint64 dataLen, data.
func encodePack(ids []string, objects map[string][]byte) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.BigEndian, uint32(len(ids)))
	for _, id := range ids {
		binary.Write(&buf, binary.BigEndian, uint32(len(id)))
		buf.WriteString(id)
		data := objects[id]
		binary.Write(&buf, binary.BigEndian, uint64(len(data)))
		buf.Write(data)
	}
	return buf.Bytes()
}

func decodePack(b []byte) (map[string][]byte, error) {
	r := bytes.NewReader(b)
	var count uint32
	if err := binary.Read(r, binary.BigEndian, &count); err != nil {
		return nil, err
	}
	out := make(map[string][]byte, count)
	for i := uint32(0); i < count; i++ {
		var idLen uint32
		if err := binary.Read(r, binary.BigEndian, &idLen); err != nil {
			return nil, err
		}
		idBuf := make([]byte, idLen)
		if _, err := io.ReadFull(r, idBuf); err != nil {
			return nil, err
		}
		var dataLen uint64
		if err := binary.Read(r, binary.BigEndian, &dataLen); err != nil {
			return nil, err
		}
		data := make([]byte, dataLen)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, err
		}
		out[string(idBuf)] = data
	}
	return out, nil
}

func hashID(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
