package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"0":      0,
		"1024":   1024,
		"100GiB": 100 << 30,
		"1.5TB":  1.5e12,
		"512mib": 512 << 20,
		"2G":     2 << 30,
		" 10 KB": 10e3,
	}
	for in, want := range cases {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "GiB", "-1GiB", "ten"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) should fail", bad)
		}
	}
	if s := FormatSize(100 << 30); s != "100GiB" {
		t.Errorf("FormatSize = %q", s)
	}
	if s := FormatSize(1536 << 20); s != "1.5GiB" {
		t.Errorf("FormatSize = %q", s)
	}
}

// The written template must decode back to exactly the defaults (with the store
// it was written with), so "every value shown is the default" stays true.
func TestTemplateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(DataStoreEnv, filepath.Join(dir, "store"))
	p, err := Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if wrote, err := WriteTemplate(p); err != nil || !wrote {
		t.Fatalf("WriteTemplate = %v, %v", wrote, err)
	}
	if wrote, _ := WriteTemplate(p); wrote {
		t.Fatal("WriteTemplate must not overwrite an existing config")
	}

	var got Settings
	if _, err := toml.DecodeFile(p.ConfigPath, &got); err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	want.Store = os.Getenv(DataStoreEnv)
	if got.Name != want.Name || got.Store != want.Store || len(got.Listen) != len(want.Listen) || got.Listen[3] != want.Listen[3] || got.Reachability != want.Reachability ||
		got.Hosting != want.Hosting || got.Backup.Redundancy != want.Backup.Redundancy ||
		got.Backup.MinFriends != want.Backup.MinFriends || len(got.Backup.Folders) != 0 ||
		got.Backup.Every != want.Backup.Every || got.Backup.RenewEvery != want.Backup.RenewEvery ||
		got.Backup.AuditEvery != want.Backup.AuditEvery || got.Retention != want.Retention {
		t.Fatalf("template decodes to %+v, want %+v", got, want)
	}
}

func TestInterval(t *testing.T) {
	for in, want := range map[string]time.Duration{"hourly": time.Hour, "daily": 24 * time.Hour, "30m": 30 * time.Minute, "6h": 6 * time.Hour} {
		var i Interval
		if err := i.UnmarshalText([]byte(in)); err != nil || time.Duration(i) != want {
			t.Errorf("%q = %v, %v", in, time.Duration(i), err)
		}
	}
	var i Interval
	if i.UnmarshalText([]byte("-1h")) == nil || i.UnmarshalText([]byte("often")) == nil {
		t.Error("bad intervals accepted")
	}
	for d, want := range map[time.Duration]string{24 * time.Hour: "24h", 30 * time.Minute: "30m", 10 * time.Second: "10s", 90 * time.Minute: "90m"} {
		if s := Interval(d).String(); s != want {
			t.Errorf("String(%v) = %q, want %q", d, s, want)
		}
	}
}

func TestAddFolder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(DataStoreEnv, filepath.Join(dir, "store"))
	p, _ := Ensure()
	a, b := filepath.Join(dir, "docs"), filepath.Join(dir, "photos")
	for _, f := range []string{a, b} {
		if added, err := AddFolder(p, f); err != nil || !added {
			t.Fatalf("AddFolder(%s) = %v, %v", f, added, err)
		}
	}
	if added, _ := AddFolder(p, a); added {
		t.Fatal("folder added twice")
	}
	s, err := Load()
	if err != nil || len(s.Backup.FolderPaths()) != 2 || s.Backup.FolderPaths()[1] != b {
		t.Fatalf("folders = %v, %v", s.Backup.Folders, err)
	}
	raw, _ := os.ReadFile(p.ConfigPath)
	if !strings.Contains(string(raw), "# How often the daemon snapshots them") {
		t.Fatal("comments were lost")
	}
}
