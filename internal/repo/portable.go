package repo

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// Renamed records a file or directory restored under a different name than
// the one in the snapshot, because this OS can't hold the original name or
// because it collides with another one on a case-insensitive filesystem.
type Renamed struct {
	From string // path in the snapshot, with "/" separators
	To   string // path it was restored to
}

// caseInsensitive is true where the default filesystem ignores case (NTFS,
// APFS), so "a.txt" and "A.txt" in one directory would overwrite each other.
var caseInsensitive = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// PortableName returns name escaped so this OS can create it. Only Windows
// restricts names beyond "/" and NUL; there, reserved characters, control
// characters, a trailing dot or space and device names such as CON are written
// as %XX, so the original is still readable.
func PortableName(name string) string {
	if runtime.GOOS != "windows" {
		return name
	}
	return windowsName(name)
}

func windowsName(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		last := i == len(name)-1
		if c < 0x20 || strings.IndexByte(`<>:"/\|?*`, c) >= 0 || (last && (c == '.' || c == ' ')) {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	out := b.String()
	if reservedWindowsName(out) {
		out = fmt.Sprintf("%%%02X", out[0]) + out[1:]
	}
	return out
}

// reservedWindowsName reports device names (CON, PRN, AUX, NUL, COM1-9,
// LPT1-9), which Windows reserves with or without an extension.
func reservedWindowsName(name string) bool {
	base := strings.ToUpper(name)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimRight(base, " ")
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
		base[3] >= '1' && base[3] <= '9'
}

// dirNames hands out names within one restored directory, so two entries
// never land on the same file.
type dirNames map[string]bool

func (d dirNames) key(name string) string {
	if caseInsensitive {
		return strings.ToLower(name)
	}
	return name
}

// claim returns the name to restore name under: its portable form, made unique
// in this directory with a " (2)", " (3)", ... suffix before the extension.
func (d dirNames) claim(name string) string {
	out := PortableName(name)
	if !d[d.key(out)] {
		d[d.key(out)] = true
		return out
	}
	ext := filepath.Ext(out)
	if ext == out {
		ext = "" // a dotfile like ".profile" has no extension
	}
	stem := strings.TrimSuffix(out, ext)
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, n, ext)
		if !d[d.key(cand)] {
			d[d.key(cand)] = true
			return cand
		}
	}
}
