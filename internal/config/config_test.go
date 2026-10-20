package config

import (
	"os"
	"path/filepath"
	"testing"

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
	if got.Name != want.Name || got.Store != want.Store || got.Listen[0] != want.Listen[0] ||
		got.Hosting != want.Hosting || got.Backup != want.Backup {
		t.Fatalf("template decodes to %+v, want %+v", got, want)
	}
}
