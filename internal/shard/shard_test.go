package shard

import (
	"bytes"
	"math/rand"
	"testing"
)

func randomData(n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewSource(42))
	r.Read(b)
	return b
}

func TestEncodeDecodeFromRandomSubset(t *testing.T) {
	data := randomData(300 * 1024) // spans several source symbols
	params, shards, err := Encode(data, 3.0)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if params.TotalShards != len(shards) {
		t.Fatalf("TotalShards=%d but got %d shards", params.TotalShards, len(shards))
	}
	if params.TotalShards <= params.SourceSymbols {
		t.Fatalf("expected redundancy: total=%d source=%d", params.TotalShards, params.SourceSymbols)
	}

	// Keep a random subset slightly above K (Raptor needs a small overhead).
	keep := params.SourceSymbols + params.SourceSymbols/10 + 5
	if keep > len(shards) {
		keep = len(shards)
	}
	r := rand.New(rand.NewSource(7))
	perm := r.Perm(len(shards))
	subset := make([]Shard, 0, keep)
	for _, idx := range perm[:keep] {
		subset = append(subset, shards[idx])
	}

	got, err := Decode(params, subset)
	if err != nil {
		t.Fatalf("Decode from %d/%d shards: %v", keep, len(shards), err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("decoded data does not match original")
	}
}

func TestDecodeInsufficientShardsFails(t *testing.T) {
	data := randomData(300 * 1024)
	params, shards, err := Encode(data, 3.0)
	if err != nil {
		t.Fatal(err)
	}
	// Strictly fewer than K shards can never decode.
	subset := shards[:params.SourceSymbols-1]
	if _, err := Decode(params, subset); err == nil {
		t.Fatal("expected decode with < K shards to fail")
	}
}

func TestEncodeRejectsBadInput(t *testing.T) {
	if _, _, err := Encode(nil, 3.0); err == nil {
		t.Fatal("expected error on empty input")
	}
	if _, _, err := Encode([]byte("data"), 0.5); err == nil {
		t.Fatal("expected error on redundancy < 1")
	}
}

func TestEncodeDoesNotMutateInput(t *testing.T) {
	data := randomData(128 * 1024)
	orig := make([]byte, len(data))
	copy(orig, data)
	if _, _, err := Encode(data, 2.0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, orig) {
		t.Fatal("Encode mutated its input slice")
	}
}
