package chunk

import (
	"bytes"
	"crypto/sha256"
	"math/rand"
	"testing"
)

func collect(t *testing.T, data []byte) [][]byte {
	t.Helper()
	var chunks [][]byte
	err := Split(bytes.NewReader(data), func(b []byte) error {
		cp := make([]byte, len(b))
		copy(cp, b)
		chunks = append(chunks, cp)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return chunks
}

func TestSplitReassembles(t *testing.T) {
	data := make([]byte, 8<<20) // 8 MiB
	rand.New(rand.NewSource(1)).Read(data)
	var buf bytes.Buffer
	for _, c := range collect(t, data) {
		buf.Write(c)
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Fatal("reassembled chunks do not equal original")
	}
}

func TestSplitIsDeterministic(t *testing.T) {
	data := make([]byte, 4<<20)
	rand.New(rand.NewSource(2)).Read(data)
	a := collect(t, data)
	b := collect(t, data)
	if len(a) != len(b) {
		t.Fatalf("chunk counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatalf("chunk %d differs between runs", i)
		}
	}
}

// TestStabilityUnderInsertion is the property that makes incremental snapshots
// work: prepending bytes changes only the chunks near the edit, so most chunks
// (by content hash) are shared with the original.
func TestStabilityUnderInsertion(t *testing.T) {
	data := make([]byte, 8<<20)
	rand.New(rand.NewSource(3)).Read(data)

	modified := append([]byte("a thousand new bytes................................"), data...)

	hashes := func(chunks [][]byte) map[[32]byte]bool {
		m := make(map[[32]byte]bool)
		for _, c := range chunks {
			m[sha256.Sum256(c)] = true
		}
		return m
	}
	orig := hashes(collect(t, data))
	mod := collect(t, modified)

	shared := 0
	for _, c := range mod {
		if orig[sha256.Sum256(c)] {
			shared++
		}
	}
	if shared < len(mod)/2 {
		t.Fatalf("expected most chunks shared after insertion, got %d/%d", shared, len(mod))
	}
}
