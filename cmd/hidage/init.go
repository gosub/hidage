package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/identity"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the config directory and generate a node identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.Ensure()
			if err != nil {
				return err
			}
			if identity.Exists(paths.IdentityPath) {
				id, err := identity.Load(paths.IdentityPath)
				if err != nil {
					return err
				}
				fmt.Printf("Identity already exists at %s\n", paths.IdentityPath)
				fmt.Printf("Public key: %x\n", id.Public)
				return nil
			}
			id, err := identity.Generate()
			if err != nil {
				return err
			}
			if err := id.Save(paths.IdentityPath); err != nil {
				return err
			}
			fmt.Printf("Initialized hidage at %s\n", paths.Dir)
			fmt.Printf("Public key: %x\n", id.Public)
			return nil
		},
	}
}
