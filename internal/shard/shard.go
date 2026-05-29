// Package shard turns an encrypted blob into a set of fountain-coded shards and
// back. It wraps the R10 Raptor codec from gofountain: the input is split into
// K source symbols and expanded into M >= K encoded shards. Any sufficiently
// large subset of shards (K plus a small coding overhead) reconstructs the
// original blob, in any order. This is what lets hidage spread redundant shards
// across many hosts and tolerate the loss of any minority of them.
package shard

import (
	"fmt"
	"math"

	fountain "github.com/google/gofountain"
)

const (
	// alignment is the Raptor symbol XOR granularity (Al), in bytes.
	alignment = 4
	// targetSymbolSize guides the choice of K so shards stay a sane size.
	targetSymbolSize = 64 * 1024
	// minSymbols / maxSymbols are the Raptor R10 bounds on K.
	minSymbols = 4
	maxSymbols = 8192
	// maxShards keeps BlockCode within the uint16 range the codec uses.
	maxShards = 1 << 16
)

// Params captures everything (besides the shards themselves) needed to decode.
// These fields are persisted in the manifest.
type Params struct {
	Algorithm     string `json:"algorithm"`
	SourceSymbols int    `json:"sourceSymbols"` // K: minimum count needed (plus overhead)
	Alignment     int    `json:"alignment"`
	MessageLen    int    `json:"messageLen"`  // exact ciphertext length, for trimming padding
	TotalShards   int    `json:"totalShards"` // M
}

// Shard is one encoded symbol. BlockCode identifies how the shard was composed
// and is required, alongside Params, to decode.
type Shard struct {
	BlockCode int64
	Data      []byte
}

// chooseK picks the number of source symbols for a message of the given length.
func chooseK(msgLen int) int {
	k := int(math.Ceil(float64(msgLen) / float64(targetSymbolSize)))
	if k < minSymbols {
		k = minSymbols
	}
	if k > maxSymbols {
		k = maxSymbols
	}
	return k
}

// Encode expands ciphertext into shards. redundancy is the ratio of total
// shards to source symbols (e.g. 3.0 emits ~3x the minimum needed). The input
// slice is not modified.
func Encode(ciphertext []byte, redundancy float64) (Params, []Shard, error) {
	if len(ciphertext) == 0 {
		return Params{}, nil, fmt.Errorf("cannot shard empty input")
	}
	if redundancy < 1 {
		return Params{}, nil, fmt.Errorf("redundancy must be >= 1, got %g", redundancy)
	}

	k := chooseK(len(ciphertext))
	m := int(math.Ceil(float64(k) * redundancy))
	if m < k {
		m = k
	}
	if m >= maxShards {
		return Params{}, nil, fmt.Errorf("too many shards (%d); reduce redundancy or input size", m)
	}

	// EncodeLTBlocks mutates its input, so hand it a copy.
	msg := make([]byte, len(ciphertext))
	copy(msg, ciphertext)

	codec := fountain.NewRaptorCodec(k, alignment)
	ids := make([]int64, m)
	for i := range ids {
		ids[i] = int64(i) // 0..K-1 are systematic source symbols, K..M-1 are repair
	}

	blocks := fountain.EncodeLTBlocks(msg, ids, codec)
	shards := make([]Shard, len(blocks))
	for i, b := range blocks {
		shards[i] = Shard{BlockCode: b.BlockCode, Data: b.Data}
	}

	return Params{
		Algorithm:     "raptor-r10",
		SourceSymbols: k,
		Alignment:     alignment,
		MessageLen:    len(ciphertext),
		TotalShards:   m,
	}, shards, nil
}

// Decode reconstructs the ciphertext from a subset of shards. It returns an
// error if too few shards are supplied to recover the message.
func Decode(p Params, shards []Shard) ([]byte, error) {
	if len(shards) < p.SourceSymbols {
		return nil, fmt.Errorf("insufficient shards: have %d, need at least %d", len(shards), p.SourceSymbols)
	}

	codec := fountain.NewRaptorCodec(p.SourceSymbols, p.Alignment)
	decoder := codec.NewDecoder(p.MessageLen)

	blocks := make([]fountain.LTBlock, len(shards))
	for i, s := range shards {
		blocks[i] = fountain.LTBlock{BlockCode: s.BlockCode, Data: s.Data}
	}

	if !decoder.AddBlocks(blocks) {
		return nil, fmt.Errorf("insufficient shards to decode: have %d, need ~%d", len(shards), p.SourceSymbols)
	}
	out := decoder.Decode()
	if out == nil {
		return nil, fmt.Errorf("decode failed despite sufficient blocks")
	}
	return out, nil
}
