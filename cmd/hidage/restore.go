package main

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"hidage/internal/archive"
	"hidage/internal/crypto"
	"hidage/internal/manifest"
	"hidage/internal/shard"
	"hidage/internal/store"
)

func newRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <manifest> <outdir>",
		Short: "Reassemble a backup from available shards",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			manifestPath, outDir := args[0], args[1]

			st, _, err := openStore()
			if err != nil {
				return err
			}
			m, err := manifest.Load(manifestPath)
			if err != nil {
				return err
			}
			if err := m.Verify(); err != nil {
				return err
			}

			// 1. Gather whatever shards are present in the store.
			var shards []shard.Shard
			for _, ref := range m.Shards {
				data, err := st.Get(ref.ID)
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				shards = append(shards, shard.Shard{BlockCode: ref.BlockCode, Data: data})
			}
			fmt.Printf("Found %d of %d shards (need ~%d)\n", len(shards), len(m.Shards), m.Fountain.SourceSymbols)

			// 2. Fountain-decode back to ciphertext.
			ciphertext, err := shard.Decode(m.Fountain, shards)
			if err != nil {
				return err
			}

			// 3. Unwrap the DEK with the passphrase and decrypt.
			pass, err := readPassphrase(false)
			if err != nil {
				return err
			}
			kek := crypto.DeriveKEK(pass, m.Crypto.Argon2)
			dek, err := crypto.UnwrapDEK(kek, m.Crypto.WrappedDEK)
			if err != nil {
				return err
			}
			blob, err := crypto.Decrypt(dek, ciphertext, m.Crypto.ChunkSize)
			if err != nil {
				return err
			}

			// 4. Integrity check against the manifest.
			if !bytes.Equal(manifest.PlaintextHash(blob), m.PlaintextSHA256) {
				return errors.New("restored data failed integrity check (plaintext hash mismatch)")
			}

			// 5. Unpack to disk.
			if err := archive.Unpack(blob, m.IsArchive, outDir, m.OriginalName); err != nil {
				return err
			}
			fmt.Printf("Restored %q into %s (verified %d bytes)\n", m.OriginalName, outDir, len(blob))
			return nil
		},
	}
}
