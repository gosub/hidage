package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/keyring"
)

func newRecoveryCodeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recovery-code",
		Short: "Print the BIP39 recovery phrase for this node's keyring",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kr, err := unlock()
			if err != nil {
				return err
			}
			code, err := kr.RecoveryCode()
			if err != nil {
				return err
			}
			fmt.Println("Write down these words and keep them safe and secret.")
			fmt.Println("Anyone with them can decrypt all your backups; with them you can")
			fmt.Println("recover everything on a new machine via `hidage recover`.")
			fmt.Println()
			fmt.Println(code)
			return nil
		},
	}
}

func newRecoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recover",
		Short: "Re-create the keyring from a recovery phrase (read from stdin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.Ensure()
			if err != nil {
				return err
			}
			if keyring.Exists(paths.KeyringPath) {
				return fmt.Errorf("a keyring already exists at %s; remove it before recovering", paths.KeyringPath)
			}

			fmt.Fprintln(os.Stderr, "Enter your recovery phrase:")
			mnemonic, err := readLine()
			if err != nil {
				return err
			}
			if strings.TrimSpace(mnemonic) == "" {
				return fmt.Errorf("no recovery phrase provided")
			}

			fmt.Fprintln(os.Stderr, "Set a passphrase to protect the restored keyring on this machine.")
			pass, err := readPassphrase(true)
			if err != nil {
				return err
			}
			kr, err := keyring.Restore(paths.KeyringPath, mnemonic, pass)
			if err != nil {
				return err
			}
			fmt.Printf("Recovered keyring at %s\n", paths.Dir)
			fmt.Printf("Public key: %x\n", kr.Identity().Public)
			return nil
		},
	}
}

// readLine reads a single line (the recovery phrase) from stdin.
func readLine() (string, error) {
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
