package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/keyring"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the user keyring (root secret: master key + identity)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.Ensure()
			if err != nil {
				return err
			}
			if keyring.Exists(paths.KeyringPath) {
				fmt.Printf("Already initialized at %s\n", paths.Dir)
				fmt.Println("Run `hidage recovery-code` to view your recovery phrase.")
				return nil
			}
			pass, err := readPassphrase(true)
			if err != nil {
				return err
			}
			kr, err := keyring.Init(paths.KeyringPath, pass)
			if err != nil {
				return err
			}
			fmt.Printf("Initialized hidage at %s\n", paths.Dir)
			fmt.Printf("Public key: %x\n", kr.Identity().Public)
			fmt.Println()
			fmt.Println("IMPORTANT: run `hidage recovery-code` and write down the phrase.")
			fmt.Println("It is the only way to recover your backups if you lose this machine")
			fmt.Println("and forget your passphrase — it cannot be reset.")
			return nil
		},
	}
}
