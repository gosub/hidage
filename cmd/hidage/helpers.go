package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"golang.org/x/term"

	"hidage/internal/config"
	"hidage/internal/friends"
	"hidage/internal/keyring"
	"hidage/internal/p2p"
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

// readPassphrase obtains the passphrase from the environment or, failing that,
// the terminal. When confirm is set the user is asked to type it twice.
func readPassphrase(confirm bool) (string, error) {
	if env := os.Getenv(passphraseEnv); env != "" {
		return env, nil
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
// requires `hidage init` (or `hidage recover`) to have run.
func unlock() (*keyring.Keyring, error) {
	paths, err := config.Resolve()
	if err != nil {
		return nil, err
	}
	if !keyring.Exists(paths.KeyringPath) {
		return nil, fmt.Errorf("no keyring found; run `hidage init` (or `hidage recover`) first")
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
		return nil, fmt.Errorf("not inside a hidage repository (run `hidage snapshot` to create one)")
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
		return nil, fmt.Errorf("not inside a hidage repository (run `hidage snapshot` to create one)")
	}
	return repo.Open(folder, nil, config.DataStoreDir())
}

// processAlive reports whether a process with this pid exists.
func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// bootstrapAddrs returns the explicit --bootstrap addresses, or, when none were
// given, every known address of every friend (as full /p2p multiaddrs), so
// networked commands need no flags once friends are added.
func bootstrapAddrs(explicit []string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	book, err := loadFriends()
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range book.List() {
		pid, err := f.PeerID()
		if err != nil {
			continue
		}
		for _, a := range f.Addrs {
			out = append(out, a+"/p2p/"+pid.String())
		}
	}
	return out
}

// joinNetwork connects node to the network: the explicit --bootstrap peers (all
// unreachable is an error), or else the friends (none online is only a note, so
// a node can start before its friends do).
func joinNetwork(ctx context.Context, node *p2p.Node, explicit []string) error {
	err := node.Bootstrap(ctx, bootstrapAddrs(explicit))
	if err != nil && len(explicit) == 0 {
		fmt.Fprintln(os.Stderr, "note: none of your friends is reachable right now")
		return nil
	}
	return err
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
