package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/identity"
	"hidage/internal/userkey"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Set up the node identity and user-level encryption key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.Ensure()
			if err != nil {
				return err
			}

			// Identity (Ed25519, signs snapshots).
			var id identity.Identity
			if identity.Exists(paths.IdentityPath) {
				if id, err = identity.Load(paths.IdentityPath); err != nil {
					return err
				}
			} else {
				if id, err = identity.Generate(); err != nil {
					return err
				}
				if err := id.Save(paths.IdentityPath); err != nil {
					return err
				}
			}

			// User-level master key (encrypts all repositories).
			if userkey.Exists(paths.MasterKeyPath) {
				fmt.Printf("Already initialized at %s\n", paths.Dir)
				fmt.Printf("Public key: %x\n", id.Public)
				return nil
			}
			pass, err := readPassphrase(true)
			if err != nil {
				return err
			}
			if _, err := userkey.Init(paths.MasterKeyPath, pass); err != nil {
				return err
			}
			fmt.Printf("Initialized hidage at %s\n", paths.Dir)
			fmt.Printf("Public key: %x\n", id.Public)
			fmt.Printf("Encryption key created (remember your passphrase — it cannot be recovered).\n")
			return nil
		},
	}
}
