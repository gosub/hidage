package shard

import (
	"bytes"
	"math/rand"
	"testing"
)

func randomData(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(42)).Read(b)
	return b
}

// TestRoundtripAcrossSizes guards against the silent-corruption class of bug:
// for a range of sizes, encoding then decoding from all shards must reproduce
// the input exactly.
func TestRoundtripAcrossSizes(t *testing.T) {
	for _, n := range []int{1024, 200_000, 400_000, 800_000, 1_200_000, 3_000_000} {
		data := randomData(n)
		params, shards, err := Encode(data, 2.0)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		got, err := Decode(params, shards)
		if err != nil {
			t.Fatalf("n=%d decode: %v", n, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("n=%d: decoded data does not match (K=%d M=%d)", n, params.DataShards, params.TotalShards)
		}
	}
}

func TestDecodeFromAnyKSubset(t *testing.T) {
	data := randomData(1_000_000)
	params, shards, err := Encode(data, 3.0)
	if err != nil {
		t.Fatal(err)
	}
	if params.TotalShards <= params.DataShards {
		t.Fatalf("expected redundancy: K=%d M=%d", params.DataShards, params.TotalShards)
	}

	// Keep a random exactly-K subset; Reed-Solomon must reconstruct from any K.
	r := rand.New(rand.NewSource(7))
	perm := r.Perm(len(shards))
	subset := make([]Shard, 0, params.DataShards)
	for _, idx := range perm[:params.DataShards] {
		subset = append(subset, shards[idx])
	}
	got, err := Decode(params, subset)
	if err != nil {
		t.Fatalf("decode from K subset: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("decoded data does not match original")
	}
}

func TestDecodeInsufficientShardsFails(t *testing.T) {
	data := randomData(1_000_000)
	params, shards, err := Encode(data, 2.0)
	if err != nil {
		t.Fatal(err)
	}
	subset := shards[:params.DataShards-1]
	if _, err := Decode(params, subset); err == nil {
		t.Fatal("expected decode with < K shards to fail")
	}
}

func TestEncodeRejectsBadInput(t *testing.T) {
	if _, _, err := Encode(nil, 2.0); err == nil {
		t.Fatal("expected error on empty input")
	}
	if _, _, err := Encode([]byte("data"), 0.5); err == nil {
		t.Fatal("expected error on redundancy < 1")
	}
}
