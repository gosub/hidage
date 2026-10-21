package pack

import (
	"bytes"
	"math/rand"
	"testing"
)

func sampleObjects() map[string][]byte {
	objs := map[string][]byte{}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 5; i++ {
		b := make([]byte, 300_000)
		r.Read(b)
		objs[hashID(b)] = b
	}
	return objs
}

func TestBuildReassembleAllShards(t *testing.T) {
	objs := sampleObjects()
	rel, blobs, err := Build(objs, 2.0, "snap")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Reassemble(rel, func(id string) ([]byte, error) {
		if b, ok := blobs[id]; ok {
			return b, nil
		}
		return nil, errMissing
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSameObjects(t, objs, got)
}

func TestReassembleSurvivesDroppedShards(t *testing.T) {
	objs := sampleObjects()
	rel, blobs, err := Build(objs, 3.0, "snap") // 3x redundancy
	if err != nil {
		t.Fatal(err)
	}
	// Drop ~half the shards of every pack — well within what 3x tolerates.
	dropped := map[string]bool{}
	for _, p := range rel.Packs {
		for i, ref := range p.Shards {
			if i%2 == 0 {
				dropped[ref.ID] = true
			}
		}
	}
	got, err := Reassemble(rel, func(id string) ([]byte, error) {
		if dropped[id] {
			return nil, errMissing
		}
		return blobs[id], nil
	})
	if err != nil {
		t.Fatalf("reassemble with half the shards dropped: %v", err)
	}
	assertSameObjects(t, objs, got)
}

func TestReassembleFailsBelowThreshold(t *testing.T) {
	objs := sampleObjects()
	rel, blobs, err := Build(objs, 2.0, "snap")
	if err != nil {
		t.Fatal(err)
	}
	// Keep only a handful of shards from the first pack — fewer than K.
	kept := 0
	keep := map[string]bool{}
	for _, ref := range rel.Packs[0].Shards {
		if kept < rel.Packs[0].Params.DataShards-1 {
			keep[ref.ID] = true
			kept++
		}
	}
	_, err = Reassemble(rel, func(id string) ([]byte, error) {
		if keep[id] {
			return blobs[id], nil
		}
		return nil, errMissing
	})
	if err == nil {
		t.Fatal("expected reassembly to fail below the shard threshold")
	}
}

func assertSameObjects(t *testing.T, want, got map[string][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d objects, want %d", len(got), len(want))
	}
	for id, w := range want {
		if !bytes.Equal(got[id], w) {
			t.Fatalf("object %s mismatch", id)
		}
	}
}

var errMissing = errMissingType{}

type errMissingType struct{}

func (errMissingType) Error() string { return "missing" }

func TestBuildIsDeterministic(t *testing.T) {
	objs := sampleObjects()
	r1, b1, err := Build(objs, 2.0, "snap")
	if err != nil {
		t.Fatal(err)
	}
	r2, b2, err := Build(objs, 2.0, "snap")
	if err != nil {
		t.Fatal(err)
	}
	if len(b1) != len(b2) {
		t.Fatalf("shard set sizes differ: %d vs %d", len(b1), len(b2))
	}
	// Same shard ids both times -> a republish renews the same shards/leases.
	for id := range b1 {
		if _, ok := b2[id]; !ok {
			t.Fatalf("shard %s present in first build but not second", id)
		}
	}
	if len(r1.Packs) != len(r2.Packs) {
		t.Fatalf("pack counts differ: %d vs %d", len(r1.Packs), len(r2.Packs))
	}
}

// Adding one object to many must leave almost every pack, and so almost every
// shard, unchanged.
func TestPacksStableUnderSmallChanges(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	objs := map[string][]byte{}
	for i := 0; i < 400; i++ {
		b := make([]byte, 50_000+r.Intn(200_000))
		r.Read(b)
		objs[hashID(b)] = b
	}
	shardSet := func(o map[string][]byte) map[string]bool {
		_, shards, err := Build(o, 2.0, "snap")
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for id := range shards {
			m[id] = true
		}
		return m
	}
	before := shardSet(objs)
	extra := make([]byte, 100_000)
	r.Read(extra)
	objs[hashID(extra)] = extra
	after := shardSet(objs)

	changed := 0
	for id := range after {
		if !before[id] {
			changed++
		}
	}
	if changed*10 > len(after) {
		t.Fatalf("%d of %d shards changed after adding one object (want < 10%%)", changed, len(after))
	}
	if groups := groupByContent(objs); len(groups) < 5 {
		t.Fatalf("only %d packs; the test needs several", len(groups))
	}
}
