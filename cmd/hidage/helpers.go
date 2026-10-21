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
// passphrase_command, the OS password store, or the terminal. When confirm is
// set (choosing a new passphrase) the command and the store are skipped and
// the user is asked to type it twice.
func readPassphrase(confirm bool) (string, error) {
	pass, _, err := lookupPassphrase(confirm, true)
	return pass, err
}

// errNoPassphrase wraps the reason no passphrase could be obtained at all (as
// opposed to a wrong one), so the daemon can fall back to hosting only.
type errNoPassphrase struct{ err error }

func (e errNoPassphrase) Error() string { return e.err.Error() }
func (e errNoPassphrase) Unwrap() error { return e.err }

// unlockKeyring loads the keyring at path with the passphrase from
// readPassphrase. A saved passphrase that no longer unlocks it is removed from
// the password store, and the terminal is asked instead.
func unlockKeyring(path string) (*keyring.Keyring, error) {
	pass, saved, err := lookupPassphrase(false, true)
	if err != nil {
		return nil, errNoPassphrase{err}
	}
	kr, err := keyring.Load(path, pass)
	if err != nil && saved {
		forgetSavedPassphrase()
		fmt.Fprintf(os.Stderr, "The passphrase saved in %s is wrong; removed it.\n", secretStoreName())
		if pass, _, err = lookupPassphrase(false, false); err != nil {
			return nil, errNoPassphrase{err}
		}
		kr, err = keyring.Load(path, pass)
	}
	return kr, err
}

// lookupPassphrase is readPassphrase, also reporting whether the passphrase
// came from the password store; useStore false skips the store.
func lookupPassphrase(confirm, useStore bool) (pass string, saved bool, err error) {
	if env := os.Getenv(passphraseEnv); env != "" {
		return env, false, nil
	}
	if settings, _ := config.Load(); settings.PassphraseCommand != "" && !confirm {
		out, err := shellCommand(settings.PassphraseCommand).Output()
		if err != nil {
			return "", false, fmt.Errorf("passphrase_command failed: %w", err)
		}
		if pass := strings.TrimRight(string(out), "\r\n"); pass != "" {
			return pass, false, nil
		}
		return "", false, errors.New("passphrase_command printed nothing")
	}
	if useStore && !confirm {
		if pass, ok := savedPassphrase(); ok {
			return pass, true, nil
		}
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", false, fmt.Errorf("no terminal for passphrase; set %s", passphraseEnv)
	}

	fmt.Fprint(os.Stderr, "Passphrase: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", false, fmt.Errorf("read passphrase: %w", err)
	}
	if len(first) == 0 {
		return "", false, errors.New("empty passphrase")
	}
	if confirm {
		fmt.Fprint(os.Stderr, "Confirm passphrase: ")
		second, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", false, fmt.Errorf("read passphrase: %w", err)
		}
		if string(first) != string(second) {
			return "", false, errors.New("passphrases do not match")
		}
	}
	return string(first), false, nil
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
	return unlockKeyring(paths.KeyringPath)
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

// printRenamed lists the files a restore put under a different name because
// this OS can't hold the original one, or because it differed from another
// only by case.
func printRenamed(renamed []repo.Renamed) {
	if len(renamed) == 0 {
		return
	}
	fmt.Printf("%d name(s) can't be used here as they are, so they were restored as:\n", len(renamed))
	for _, r := range renamed {
		fmt.Printf("  %s -> %s\n", r.From, r.To)
	}
}
