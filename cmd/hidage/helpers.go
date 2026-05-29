package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"

	"hidage/internal/config"
	"hidage/internal/store"
)

// passphraseEnv lets automation (and tests) bypass the interactive prompt.
const passphraseEnv = "HIDAGE_PASSPHRASE"

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

// openStore resolves the config paths and opens the local shard store.
func openStore() (*store.FSStore, config.Paths, error) {
	paths, err := config.Ensure()
	if err != nil {
		return nil, config.Paths{}, err
	}
	st, err := store.NewFSStore(paths.StoreDir)
	if err != nil {
		return nil, config.Paths{}, err
	}
	return st, paths, nil
}
