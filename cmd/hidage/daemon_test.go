package main

import (
	"bytes"
	"fmt"
	"testing"

	"hidage/internal/pack"
)

func TestPrefixWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &prefixWriter{prefix: "[docs] ", w: &buf}
	fmt.Fprintf(w, "  bob: ok\npart")
	fmt.Fprintf(w, "ial\n")
	if got := buf.String(); got != "[docs] bob: ok\n[docs] partial\n" {
		t.Fatalf("got %q", got)
	}
}

func TestPlaceShards(t *testing.T) {
	rel := &pack.Release{}
	for p := 0; p < 20; p++ {
		var desc pack.PackDesc
		for i := 0; i < 6; i++ {
			desc.Shards = append(desc.Shards, pack.ShardRef{ID: fmt.Sprintf("p%d-s%d", p, i), Index: i})
		}
		rel.Packs = append(rel.Packs, desc)
	}
	owners := func(keys []string) map[string]string {
		m := map[string]string{}
		for i, ids := range placeShards(rel, keys) {
			for _, id := range ids {
				m[id] = keys[i]
			}
		}
		return m
	}
	three := owners([]string{"a", "b", "c"})
	// Same inputs, same placement, regardless of peer order.
	again := owners([]string{"c", "a", "b"})
	for id, k := range three {
		if again[id] != k {
			t.Fatalf("shard %s moved from %s to %s with the same peers", id, k, again[id])
		}
	}
	// Each pack's 6 shards spread 2/2/2 over 3 peers.
	for p := 0; p < 20; p++ {
		count := map[string]int{}
		for i := 0; i < 6; i++ {
			count[three[fmt.Sprintf("p%d-s%d", p, i)]]++
		}
		if len(count) != 3 {
			t.Fatalf("pack %d spread over %d peers, want 3", p, len(count))
		}
	}
	if len(owners(nil)) != 0 {
		t.Fatal("no peers should place nothing")
	}
}
