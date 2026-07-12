package main

import "testing"

// fakeRep marks the listed peers as failing audits.
type fakeRep map[string]bool

func (f fakeRep) Bad(pubHex string) bool { return f[pubHex] }

func TestAssessPeers(t *testing.T) {
	peers := map[string][]string{"a": nil, "b": nil, "c": nil, "d": nil}
	dist := distribution{Redundancy: 2.0, Peers: peers}

	// 4 peers at 2.0x: tolerate floor(4 * (1 - 1/2)) = 2.
	tol, failing := assessPeers(dist, fakeRep{})
	if tol != 2 || failing != 0 {
		t.Fatalf("no failures: tolerate=%d failing=%d, want 2,0", tol, failing)
	}

	// One failing peer is within tolerance.
	tol, failing = assessPeers(dist, fakeRep{"a": true})
	if tol != 2 || failing != 1 {
		t.Fatalf("one failure: tolerate=%d failing=%d, want 2,1", tol, failing)
	}

	// Three failing exceeds the tolerance of 2 — the danger case.
	_, failing = assessPeers(dist, fakeRep{"a": true, "b": true, "c": true})
	if failing != 3 {
		t.Fatalf("three failures: failing=%d, want 3", failing)
	}

	// Single peer at 2.0x has zero fault tolerance.
	solo := distribution{Redundancy: 2.0, Peers: map[string][]string{"a": nil}}
	if tol, _ := assessPeers(solo, fakeRep{}); tol != 0 {
		t.Fatalf("single peer tolerate=%d, want 0", tol)
	}
}
