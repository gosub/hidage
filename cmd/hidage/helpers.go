package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"

	"hidage/internal/config"
	"hidage/internal/identity"
	"hidage/internal/repo"
)

// passphraseEnv lets automation (and tests) bypass the interactive prompt.
const passphraseEnv = "HIDAGE_PASSPHRASE"

// repoFlag is bound to the persistent --repo flag (empty = default location).
var repoFlag string

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

// repoDir resolves the repository directory from --repo or the default.
func repoDir() (string, error) {
	if repoFlag != "" {
		return repoFlag, nil
	}
	paths, err := config.Resolve()
	if err != nil {
		return "", err
	}
	return paths.DefaultRepoDir, nil
}

// openRepo opens an existing repository, prompting for the passphrase. It fails
// if no repository exists yet.
func openRepo() (*repo.Repo, error) {
	dir, err := repoDir()
	if err != nil {
		return nil, err
	}
	if !repo.Exists(dir) {
		return nil, fmt.Errorf("no repository at %s (take a snapshot to create one)", dir)
	}
	pass, err := readPassphrase(false)
	if err != nil {
		return nil, err
	}
	return repo.Open(dir, pass)
}
