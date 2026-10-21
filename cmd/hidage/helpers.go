package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"golang.org/x/term"

	"hidage/internal/config"
	"hidage/internal/friends"
	"hidage/internal/keyring"
	"hidage/internal/repo"
)

// passphraseEnv lets automation (and tests) bypass the interactive prompt.
const passphraseEnv = "HIDAGE_PASSPHRASE"

// chdirFlag is bound to the persistent -C flag: the directory to operate from
// (repository discovery walks up from here). Empty means the current directory.
var chdirFlag string

// startDir is where repository discovery begins.
func startDir() string {
	if chdirFlag != "" {
		return chdirFlag
	}
	return "."
}

// readPassphrase obtains the passphrase from the environment, the configured
// passphrase_command, or the terminal. When confirm is set (choosing a new
// passphrase) the command is skipped and the user is asked to type it twice.
func readPassphrase(confirm bool) (string, error) {
	if env := os.Getenv(passphraseEnv); env != "" {
		return env, nil
	}
	if settings, _ := config.Load(); settings.PassphraseCommand != "" && !confirm {
		out, err := shellCommand(settings.PassphraseCommand).Output()
		if err != nil {
			return "", fmt.Errorf("passphrase_command failed: %w", err)
		}
		if pass := strings.TrimRight(string(out), "\r\n"); pass != "" {
			return pass, nil
		}
		return "", errors.New("passphrase_command printed nothing")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("no terminal for passphrase; set %s", passphraseEnv)
	}

	fmt.Fprint(os.Stderr, "Passphrase: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	if len(first) == 0 {
		return "", errors.New("empty passphrase")
	}
	if confirm {
		fmt.Fprint(os.Stderr, "Confirm passphrase: ")
		second, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read passphrase: %w", err)
		}
		if string(first) != string(second) {
			return "", errors.New("passphrases do not match")
		}
	}
	return string(first), nil
}

// unlock loads and unlocks the user keyring, prompting for the passphrase. It
// requires `hidage init` (or `hidage init --recover`) to have run.
func unlock() (*keyring.Keyring, error) {
	paths, err := config.Resolve()
	if err != nil {
		return nil, err
	}
	if !keyring.Exists(paths.KeyringPath) {
		return nil, fmt.Errorf("no keyring found; run `hidage init` (or `hidage init --recover`) first")
	}
	pass, err := readPassphrase(false)
	if err != nil {
		return nil, err
	}
	return keyring.Load(paths.KeyringPath, pass)
}

// discoverRepo unlocks the keyring and opens the repository found by walking up
// from the start directory.
func discoverRepo() (*repo.Repo, error) {
	folder := repo.Find(startDir())
	if folder == "" {
		return nil, fmt.Errorf("not inside a hidage repository (run `hidage backup <folder>` to create one)")
	}
	kr, err := unlock()
	if err != nil {
		return nil, err
	}
	return repo.Open(folder, kr.MasterKey(), config.DataStoreDir())
}

// discoverRepoNoKey opens the repository without unlocking the keyring, for
// commands that only touch plaintext snapshot metadata (snapshots, forget) and
// never decrypt object data — so they skip the passphrase prompt and the Argon2id
// cost. Snapshots are self-verifying (each carries its owner key + signature).
func discoverRepoNoKey() (*repo.Repo, error) {
	folder := repo.Find(startDir())
	if folder == "" {
		return nil, fmt.Errorf("not inside a hidage repository (run `hidage backup <folder>` to create one)")
	}
	return repo.Open(folder, nil, config.DataStoreDir())
}

// friendAddrInfos returns every friend with known addresses as libp2p address
// info (used as relay candidates).
func friendAddrInfos(book *friends.Book) []peer.AddrInfo {
	var out []peer.AddrInfo
	for _, f := range book.List() {
		pid, err := f.PeerID()
		if err != nil || len(f.Addrs) == 0 {
			continue
		}
		ai := peer.AddrInfo{ID: pid}
		for _, a := range f.Addrs {
			if ma, err := multiaddr.NewMultiaddr(a); err == nil {
				ai.Addrs = append(ai.Addrs, ma)
			}
		}
		out = append(out, ai)
	}
	return out
}

// hasPublicInterface reports whether any network interface has a public
// (globally routable) IP address.
func hasPublicInterface() bool {
	addrs, err := manet.InterfaceMultiaddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if manet.IsPublicAddr(a) {
			return true
		}
	}
	return false
}
