package manifest

import (
	"testing"
	"time"

	"hidage/internal/identity"
)

func sampleManifest(t *testing.T) (*Manifest, identity.Identity) {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	m := &Manifest{
		Version:         Version,
		CreatedAt:       time.Now().UTC(),
		OriginalName:    "data",
		PlaintextSize:   42,
		PlaintextSHA256: PlaintextHash([]byte("data")),
		Shards: []ShardRef{
			{ID: "abc", BlockCode: 0, Size: 10},
			{ID: "def", BlockCode: 1, Size: 10},
		},
	}
	return m, id
}

func TestSignVerify(t *testing.T) {
	m, id := sampleManifest(t)
	if err := m.Sign(id.Private); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(); err != nil {
		t.Fatalf("Verify after Sign: %v", err)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	m, id := sampleManifest(t)
	if err := m.Sign(id.Private); err != nil {
		t.Fatal(err)
	}
	m.PlaintextSize = 9999 // tamper after signing
	if err := m.Verify(); err == nil {
		t.Fatal("expected Verify to fail after tampering")
	}
}

func TestVerifyUnsignedFails(t *testing.T) {
	m, _ := sampleManifest(t)
	if err := m.Verify(); err == nil {
		t.Fatal("expected unsigned manifest to fail verification")
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	m, id := sampleManifest(t)
	if err := m.Sign(id.Private); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/m.hmanifest"
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Verify(); err != nil {
		t.Fatalf("Verify after Save/Load: %v", err)
	}
}
