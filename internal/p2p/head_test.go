package p2p

import "testing"

func TestHeadTimeStrictlyIncreases(t *testing.T) {
	prev := headTime()
	for i := 0; i < 10000; i++ {
		now := headTime()
		if now <= prev {
			t.Fatalf("headTime went from %d to %d", prev, now)
		}
		prev = now
	}
}
