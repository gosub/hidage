// Package shard adds erasure-coding redundancy: a blob is split into M shards of
// which any K reconstruct it, so data survives losing up to M−K shards (e.g.
// the peers holding them). It uses Reed-Solomon (klauspost/reedsolomon), which
// gives exact K-of-M recovery — feed any K present shards (the rest marked
// missing) and the original is rebuilt. M is fixed at encode time, which is all
// hidage needs for distributing a pack across a known set of peers.
package shard

import (
	"bytes"
	"fmt"
	"math"

	"github.com/klauspost/reedsolomon"
)

const (
	// targetShardSize guides how many data shards a blob splits into.
	targetShardSize = 256 * 1024
	// minDataShards is the floor on K.
	minDataShards = 2
	// maxTotalShards is the Reed-Solomon limit (GF(2^8)).
	maxTotalShards = 255
)

// Params captures everything (besides the shards) needed to decode. Persisted in
// the pack descriptor.
type Params struct {
	Algorithm   string `json:"algorithm"`
	DataShards  int    `json:"dataShards"`  // K: the minimum needed to reconstruct
	TotalShards int    `json:"totalShards"` // M
	MessageLen  int    `json:"messageLen"`  // exact input length, to trim shard padding
}

// Shard is one encoded shard, identified by its index 0..M-1.
type Shard struct {
	Index int
	Data  []byte
}

// chooseShards picks K (data shards) and M (total) for a blob of the given
// length and redundancy, within Reed-Solomon's limits.
func chooseShards(msgLen int, redundancy float64) (k, m int, err error) {
	k = int(math.Ceil(float64(msgLen) / float64(targetShardSize)))
	if k < minDataShards {
		k = minDataShards
	}
	if k > msgLen { // can't have more data shards than bytes
		k = msgLen
	}
	// Keep M within the field limit.
	if maxK := int(float64(maxTotalShards) / redundancy); k > maxK {
		k = maxK
	}
	if k < 1 {
		return 0, 0, fmt.Errorf("input too small to shard")
	}
	m = int(math.Ceil(float64(k) * redundancy))
	if m <= k {
		m = k + 1 // always at least one parity shard
	}
	if m > maxTotalShards {
		m = maxTotalShards
	}
	return k, m, nil
}

// Encode splits data into K data shards and M−K parity shards (total M); any K
// of the M reconstruct it. redundancy is the ratio M/K (e.g. 2.0).
func Encode(data []byte, redundancy float64) (Params, []Shard, error) {
	if len(data) == 0 {
		return Params{}, nil, fmt.Errorf("cannot shard empty input")
	}
	if redundancy < 1 {
		return Params{}, nil, fmt.Errorf("redundancy must be >= 1, got %g", redundancy)
	}
	k, m, err := chooseShards(len(data), redundancy)
	if err != nil {
		return Params{}, nil, err
	}
	enc, err := reedsolomon.New(k, m-k)
	if err != nil {
		return Params{}, nil, err
	}
	shards, err := enc.Split(data)
	if err != nil {
		return Params{}, nil, err
	}
	if err := enc.Encode(shards); err != nil {
		return Params{}, nil, err
	}
	out := make([]Shard, len(shards))
	for i, s := range shards {
		cp := make([]byte, len(s))
		copy(cp, s)
		out[i] = Shard{Index: i, Data: cp}
	}
	return Params{
		Algorithm:   "reed-solomon",
		DataShards:  k,
		TotalShards: m,
		MessageLen:  len(data),
	}, out, nil
}

// Decode reconstructs the original data from a subset of shards. It needs at
// least DataShards of them; missing shards are recovered.
func Decode(p Params, have []Shard) ([]byte, error) {
	if len(have) < p.DataShards {
		return nil, fmt.Errorf("insufficient shards: have %d, need at least %d", len(have), p.DataShards)
	}
	enc, err := reedsolomon.New(p.DataShards, p.TotalShards-p.DataShards)
	if err != nil {
		return nil, err
	}
	shards := make([][]byte, p.TotalShards)
	for _, s := range have {
		if s.Index >= 0 && s.Index < p.TotalShards {
			shards[s.Index] = s.Data
		}
	}
	if err := enc.ReconstructData(shards); err != nil {
		return nil, fmt.Errorf("reconstruct: %w", err)
	}
	var buf bytes.Buffer
	if err := enc.Join(&buf, shards, p.MessageLen); err != nil {
		return nil, fmt.Errorf("join: %w", err)
	}
	return buf.Bytes(), nil
}
