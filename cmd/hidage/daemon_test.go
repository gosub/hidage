package main

import (
	"bytes"
	"fmt"
	"testing"
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
