package main

import (
	"errors"
	"os"
	"runtime"

	secrets "github.com/zalando/go-keyring"

	"hidage/internal/config"
)

// The keyring passphrase can be saved in the OS password store (Windows
// Credential Manager, the macOS Keychain, the Linux Secret Service), so the
// daemon can take backups unattended without a passphrase_command.
const secretService = "hidage"

// secretAccount names the saved passphrase after the config directory, so
// several hidage setups on one machine (see XDG_CONFIG_HOME) don't collide.
func secretAccount() string {
	paths, err := config.Resolve()
	if err != nil {
		return "default"
	}
	return paths.Dir
}

// secretStoreName is how the password store is called on this OS.
func secretStoreName() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows Credential Manager"
	case "darwin":
		return "the macOS Keychain"
	}
	return "the Secret Service (GNOME Keyring, KWallet)"
}

// secretStoreAvailable reports whether asking the password store can work. On
// Linux it needs a D-Bus session; without one, asking could try to start a bus.
func secretStoreAvailable() bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return true
	}
	return os.Getenv("DBUS_SESSION_BUS_ADDRESS") != ""
}

// savedPassphrase returns the passphrase saved in the password store, if any.
func savedPassphrase() (string, bool) {
	if !secretStoreAvailable() {
		return "", false
	}
	pass, err := secrets.Get(secretService, secretAccount())
	return pass, err == nil && pass != ""
}

// savePassphrase saves pass in the password store.
func savePassphrase(pass string) error {
	if !secretStoreAvailable() {
		return errors.New("no password store available (no D-Bus session)")
	}
	return secrets.Set(secretService, secretAccount(), pass)
}

// forgetSavedPassphrase removes a saved passphrase, e.g. one that no longer
// unlocks the keyring.
func forgetSavedPassphrase() {
	if secretStoreAvailable() {
		secrets.Delete(secretService, secretAccount())
	}
}
