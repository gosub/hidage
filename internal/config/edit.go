package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	sectionLine = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*(#.*)?$`)
	foldersLine = regexp.MustCompile(`^\s*folders\s*=`)
)

// AddFolder adds folder to backup.folders in config.toml (writing the template
// first if there is no config yet), keeping the rest of the file, comments
// included, as it is. It reports false if the folder was already listed.
// folders must be written on one line, as the template does.
func AddFolder(p Paths, folder string) (bool, error) {
	abs, err := filepath.Abs(folder)
	if err != nil {
		return false, err
	}
	if _, err := WriteTemplate(p); err != nil {
		return false, err
	}
	s, err := loadFile(p.ConfigPath)
	if err != nil {
		return false, err
	}
	for _, f := range s.Backup.FolderPaths() {
		if fa, err := filepath.Abs(f); err == nil && fa == abs {
			return false, nil
		}
	}
	folders := append(append([]string{}, s.Backup.Folders...), homeRelative(abs))

	raw, err := os.ReadFile(p.ConfigPath)
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(raw), "\n")
	section, backupAt, replaced := "", -1, false
	for i, ln := range lines {
		if m := sectionLine.FindStringSubmatch(ln); m != nil {
			section = strings.TrimSpace(m[1])
			if section == "backup" {
				backupAt = i
			}
			continue
		}
		if section == "backup" && foldersLine.MatchString(ln) {
			if strings.Count(ln, "[") != strings.Count(ln, "]") {
				return false, fmt.Errorf("%s: write folders on one line to let hidage edit it", p.ConfigPath)
			}
			lines[i] = renderFolders(folders)
			replaced = true
			break
		}
	}
	switch {
	case replaced:
	case backupAt >= 0:
		lines = append(lines[:backupAt+1], append([]string{renderFolders(folders)}, lines[backupAt+1:]...)...)
	default:
		lines = append(lines, "[backup]", renderFolders(folders), "")
	}
	out := strings.Join(lines, "\n")
	if err := os.WriteFile(p.ConfigPath+".tmp", []byte(out), 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(p.ConfigPath+".tmp", p.ConfigPath); err != nil {
		return false, err
	}
	reset()
	return true, nil
}

func renderFolders(folders []string) string {
	q := make([]string, len(folders))
	for i, f := range folders {
		q[i] = fmt.Sprintf("%q", f)
	}
	return "folders = [" + strings.Join(q, ", ") + "]"
}

// homeRelative writes paths under the home directory as "~/…".
func homeRelative(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return p
}
