package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
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

func TestWindowsTask(t *testing.T) {
	task, err := windowsTask(`C:\Program Files\hidage & co\hidage.exe`, `C:\Users\a\AppData\Local\hidage\daemon.log`)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Actions struct {
			Exec struct {
				Command   string
				Arguments string
			}
		}
	}
	// The declaration says UTF-16 (as the file is written); parse the text as is.
	body := strings.Replace(task, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("task XML does not parse: %v", err)
	}
	want := `--headless cmd.exe /C ""C:\Program Files\hidage & co\hidage.exe" daemon >> "C:\Users\a\AppData\Local\hidage\daemon.log" 2>&1"`
	if parsed.Actions.Exec.Command != "conhost.exe" || parsed.Actions.Exec.Arguments != want {
		t.Fatalf("exec = %q %q", parsed.Actions.Exec.Command, parsed.Actions.Exec.Arguments)
	}
	enc := utf16File("<a>")
	if enc != "\xff\xfe<\x00a\x00>\x00" {
		t.Fatalf("utf16File = %q", enc)
	}
}
