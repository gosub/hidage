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
