package p2p

import "testing"

func TestRecordTimeStrictlyIncreases(t *testing.T) {
	prev := recordTime()
	for i := 0; i < 10000; i++ {
		now := recordTime()
		if now <= prev {
			t.Fatalf("recordTime went from %d to %d", prev, now)
		}
		prev = now
	}
}
