package accounting

import (
	"path/filepath"
	"testing"
)

const peer = "bb"

func TestFreeQuotaThenRatio(t *testing.T) {
	l, _ := Load(filepath.Join(t.TempDir(), "acct.json"))
	ratio, free := 3.0, int64(100)

	// With no reciprocation, accept up to the free quota.
	if !l.Allow(peer, 100, ratio, free) {
		t.Fatal("should accept up to the free quota")
	}
	if l.Allow(peer, 101, ratio, free) {
		t.Fatal("should reject beyond the free quota with no reciprocation")
	}

	// After the peer stores 50 for us, budget = ratio*50 + free = 250.
	l.AddTheyStoreForUs(peer, 50)
	if !l.Allow(peer, 250, ratio, free) {
		t.Fatal("budget should be ratio*reciprocated + free")
	}
	if l.Allow(peer, 251, ratio, free) {
		t.Fatal("should reject beyond ratio*reciprocated + free")
	}
}

func TestOverBudgetAfterStoring(t *testing.T) {
	l, _ := Load(filepath.Join(t.TempDir(), "acct.json"))
	ratio, free := 2.0, int64(10)

	l.AddStoredForThem(peer, 10) // at the free limit
	if l.Allow(peer, 1, ratio, free) {
		t.Fatal("peer at free limit with no reciprocation should be over budget")
	}
	// Reciprocate, raising the budget.
	l.AddTheyStoreForUs(peer, 100) // budget now 2*100+10 = 210
	if !l.Allow(peer, 1, ratio, free) {
		t.Fatal("reciprocation should reopen budget")
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acct.json")
	l, _ := Load(path)
	l.AddStoredForThem(peer, 123)
	l.AddTheyStoreForUs(peer, 456)

	reloaded, _ := Load(path)
	entries := reloaded.List()
	if len(entries) != 1 || entries[0].StoredForThem != 123 || entries[0].TheyStoreForUs != 456 {
		t.Fatalf("persistence lost data: %+v", entries)
	}
}
