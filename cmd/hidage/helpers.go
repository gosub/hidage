package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"

	"hidage/internal/config"
	"hidage/internal/identity"
	"hidage/internal/repo"
	"hidage/internal/userkey"
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

// loadIdentity returns the node identity, requiring `hidage init` to have run.
func loadIdentity() (identity.Identity, error) {
	paths, err := config.Resolve()
	if err != nil {
		return identity.Identity{}, err
	}
	if !identity.Exists(paths.IdentityPath) {
		return identity.Identity{}, fmt.Errorf("no identity found; run `hidage init` first")
	}
	return identity.Load(paths.IdentityPath)
}

// unlockMasterKey prompts for the passphrase and unwraps the user-level master
// key. It requires `hidage init` to have been run.
func unlockMasterKey() ([]byte, error) {
	paths, err := config.Resolve()
	if err != nil {
		return nil, err
	}
	if !userkey.Exists(paths.MasterKeyPath) {
		return nil, fmt.Errorf("no encryption key found; run `hidage init` first")
	}
	pass, err := readPassphrase(false)
	if err != nil {
		return nil, err
	}
	return userkey.Load(paths.MasterKeyPath, pass)
}

// discoverRepo unlocks the master key and opens the repository found by walking
// up from the start directory.
func discoverRepo() (*repo.Repo, error) {
	folder := repo.Find(startDir())
	if folder == "" {
		return nil, fmt.Errorf("not inside a hidage repository (run `hidage snapshot` to create one)")
	}
	mk, err := unlockMasterKey()
	if err != nil {
		return nil, err
	}
	return repo.Open(folder, mk, config.DataStoreDir())
}
