package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/keyring"
	"hidage/internal/store"
)

func newInitCmd() *cobra.Command {
	var recover bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up this machine: keyring, settings and data store (--recover: from your phrase)",
		Long: `Create this machine's keyring (one root secret: the master key that encrypts your
backups and the identity your friends know you by), a commented config.toml and the
data store.

With --recover, rebuild the keyring from the 24-word recovery phrase (read from stdin)
instead, e.g. on a new machine; then "hidage restore --friends --from <a friend's card>".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.Ensure()
			if err != nil {
				return err
			}
			if wrote, err := config.WriteTemplate(paths); err != nil {
				return err
			} else if wrote {
				fmt.Printf("Wrote settings to %s\n", paths.ConfigPath)
			}
			storeDir, err := config.EnsureDataStore()
			if err != nil {
				return err
			}
			if err := store.CheckHardLinks(storeDir); err != nil {
				return err
			}
			if keyring.Exists(paths.KeyringPath) {
				if recover {
					return fmt.Errorf("a keyring already exists at %s; remove it before recovering", paths.KeyringPath)
				}
				fmt.Printf("Already initialized at %s\n", paths.Dir)
				fmt.Println("Run `hidage id --kit` to see your recovery phrase.")
				return nil
			}
			var kr *keyring.Keyring
			if recover {
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
				if kr, err = keyring.Restore(paths.KeyringPath, mnemonic, pass); err != nil {
					return err
				}
				fmt.Printf("Recovered keyring at %s\n", paths.Dir)
			} else {
				pass, err := readPassphrase(true)
				if err != nil {
					return err
				}
				if kr, err = keyring.Init(paths.KeyringPath, pass); err != nil {
					return err
				}
				fmt.Printf("Initialized hidage at %s\n", paths.Dir)
			}
			fmt.Printf("Public key: %x\n", kr.Identity().Public)
			fmt.Printf("Data store: %s\n", storeDir)
			fmt.Println()
			if recover {
				fmt.Println("Next: hidage restore --friends --from <a friend's contact card> --to <dir>")
				return nil
			}
			fmt.Println("IMPORTANT: run `hidage id --kit` and keep what it prints somewhere safe and")
			fmt.Println("offline. The recovery phrase is the only way to get your backups back if you")
			fmt.Println("lose this machine and forget your passphrase; it cannot be reset.")
			fmt.Println()
			fmt.Println("Next: `hidage friend invite` to pair with a friend, then `hidage backup <folder>`.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&recover, "recover", false, "rebuild the keyring from your recovery phrase (stdin)")
	return cmd
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
