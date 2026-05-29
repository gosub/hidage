package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"

	"hidage/internal/config"
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
