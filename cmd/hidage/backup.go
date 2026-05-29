package main

import (
	"fmt"
	"math"
	"time"

	"github.com/spf13/cobra"

	"hidage/internal/archive"
	"hidage/internal/crypto"
	"hidage/internal/identity"
	"hidage/internal/manifest"
	"hidage/internal/shard"
)

func newBackupCmd() *cobra.Command {
	var redundancy float64
	var out string

	cmd := &cobra.Command{
		Use:   "backup <path>",
		Short: "Encrypt, fountain-shard, and store a file or folder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]

			st, paths, err := openStore()
			if err != nil {
				return err
			}
			if !identity.Exists(paths.IdentityPath) {
				return fmt.Errorf("no identity found; run `hidage init` first")
			}
			id, err := identity.Load(paths.IdentityPath)
			if err != nil {
				return err
			}

			// 1. Pack the input into a single blob.
			blob, isArchive, name, err := archive.Pack(path)
			if err != nil {
				return err
			}
			plaintextHash := manifest.PlaintextHash(blob)

			// 2. Encrypt with a fresh DEK; wrap the DEK under a passphrase KEK.
			pass, err := readPassphrase(true)
			if err != nil {
				return err
			}
			dek, err := crypto.NewDEK()
			if err != nil {
				return err
			}
			ciphertext, err := crypto.Encrypt(dek, blob, crypto.DefaultChunkSize)
			if err != nil {
				return err
			}
			argonParams, err := crypto.DefaultArgon2Params()
			if err != nil {
				return err
			}
			kek := crypto.DeriveKEK(pass, argonParams)
			wrappedDEK, err := crypto.WrapDEK(kek, dek)
			if err != nil {
				return err
			}

			// 3. Fountain-shard the ciphertext.
			fparams, shards, err := shard.Encode(ciphertext, redundancy)
			if err != nil {
				return err
			}

			// 4. Store shards content-addressed; collect references.
			refs := make([]manifest.ShardRef, len(shards))
			for i, s := range shards {
				sid, err := st.Put(s.Data)
				if err != nil {
					return err
				}
				refs[i] = manifest.ShardRef{ID: sid, BlockCode: s.BlockCode, Size: len(s.Data)}
			}

			// 5. Build and sign the manifest.
			m := &manifest.Manifest{
				Version:         manifest.Version,
				CreatedAt:       time.Now().UTC(),
				OriginalName:    name,
				IsArchive:       isArchive,
				PlaintextSize:   int64(len(blob)),
				PlaintextSHA256: plaintextHash,
				Crypto: manifest.CryptoParams{
					Algorithm:  "chacha20poly1305-chunked",
					Argon2:     argonParams,
					WrappedDEK: wrappedDEK,
					ChunkSize:  crypto.DefaultChunkSize,
				},
				Fountain: fparams,
				Shards:   refs,
			}
			if err := m.Sign(id.Private); err != nil {
				return err
			}

			if out == "" {
				out = name + ".hmanifest"
			}
			if err := m.Save(out); err != nil {
				return err
			}

			minNeeded := fparams.SourceSymbols
			tolerable := fparams.TotalShards - int(math.Ceil(float64(minNeeded)*1.05))
			if tolerable < 0 {
				tolerable = 0
			}
			fmt.Printf("Backed up %q (%d bytes plaintext)\n", name, len(blob))
			fmt.Printf("  shards: %d total, ~%d needed to restore\n", fparams.TotalShards, minNeeded)
			fmt.Printf("  redundancy: %.1fx, can tolerate losing up to ~%d shards\n", redundancy, tolerable)
			fmt.Printf("  manifest: %s\n", out)
			return nil
		},
	}

	cmd.Flags().Float64Var(&redundancy, "redundancy", 3.0, "ratio of total shards to minimum needed (>=1)")
	cmd.Flags().StringVar(&out, "out", "", "manifest output path (default <name>.hmanifest)")
	return cmd
}
