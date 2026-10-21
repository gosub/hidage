package repo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsName(t *testing.T) {
	for in, want := range map[string]string{
		"report.txt":  "report.txt",
		"a:b":         "a%3Ab",
		`back\slash`:  "back%5Cslash",
		"what?":       "what%3F",
		"trailing.":   "trailing%2E",
		"space ":      "space%20",
		"CON":         "%43ON",
		"con.txt":     "%63on.txt",
		"COM1.log":    "%43OM1.log",
		"COM0":        "COM0",
		"console.txt": "console.txt",
		"tab\there":   "tab%09here",
	} {
		if got := windowsName(in); got != want {
			t.Errorf("windowsName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaimUnique(t *testing.T) {
	d := dirNames{}
	a := d.claim("notes.txt")
	b := d.claim("notes.txt")
	c := d.claim(".profile")
	e := d.claim(".profile")
	if a != "notes.txt" || b != "notes (2).txt" || c != ".profile" || e != ".profile (2)" {
		t.Fatalf("got %q %q %q %q", a, b, c, e)
	}
}

func TestRestoreReportsCaseCollisions(t *testing.T) {
	if caseInsensitive {
		t.Skip("needs a case-sensitive filesystem to create both files")
	}
	r, priv, folder := newRepo(t)
	writeFile(t, filepath.Join(folder, "A.txt"), "upper")
	snap, _, err := r.Snapshot(priv, false)
	if err != nil {
		t.Fatal(err)
	}
	caseInsensitive = true
	defer func() { caseInsensitive = false }()

	out := t.TempDir()
	rs := &Restorer{Loader: r.Loader()}
	if err := rs.All(snap, out); err != nil {
		t.Fatal(err)
	}
	want := Renamed{From: "data/a.txt", To: filepath.Join(out, "data", "a (2).txt")}
	if len(rs.Renamed) != 1 || rs.Renamed[0] != want {
		t.Fatalf("Renamed = %+v, want [%+v]", rs.Renamed, want)
	}
	for name, content := range map[string]string{"A.txt": "upper", "a (2).txt": "alpha"} {
		got, err := os.ReadFile(filepath.Join(out, "data", name))
		if err != nil || string(got) != content {
			t.Fatalf("%s = %q, %v; want %q", name, got, err, content)
		}
	}
}
