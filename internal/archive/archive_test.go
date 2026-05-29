package archive

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPackUnpackFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "hello.txt")
	content := []byte("hello hidage")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	blob, isArchive, name, err := Pack(src)
	if err != nil {
		t.Fatal(err)
	}
	if isArchive {
		t.Fatal("single file should not be an archive")
	}
	if name != "hello.txt" {
		t.Fatalf("name = %q", name)
	}

	out := t.TempDir()
	if err := Unpack(blob, isArchive, out, name); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("file content mismatch")
	}
}

func TestPackUnpackDir(t *testing.T) {
	src := t.TempDir()
	tree := filepath.Join(src, "mydata")
	mustWrite(t, filepath.Join(tree, "a.txt"), "alpha")
	mustWrite(t, filepath.Join(tree, "sub", "b.txt"), "bravo")
	mustWrite(t, filepath.Join(tree, "sub", "deep", "c.txt"), "charlie")

	blob, isArchive, name, err := Pack(tree)
	if err != nil {
		t.Fatal(err)
	}
	if !isArchive {
		t.Fatal("directory should be an archive")
	}
	if name != "mydata" {
		t.Fatalf("name = %q", name)
	}

	out := t.TempDir()
	if err := Unpack(blob, isArchive, out, name); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"a.txt":          "alpha",
		"sub/b.txt":      "bravo",
		"sub/deep/c.txt": "charlie",
	} {
		got, err := os.ReadFile(filepath.Join(out, "mydata", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", rel, got, want)
		}
	}
}

func TestPackDirIsDeterministic(t *testing.T) {
	src := t.TempDir()
	tree := filepath.Join(src, "d")
	mustWrite(t, filepath.Join(tree, "one"), "1")
	mustWrite(t, filepath.Join(tree, "two"), "2")

	first, _, _, err := Pack(tree)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := Pack(tree)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("packing the same tree twice produced different bytes")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
