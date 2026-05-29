# hidage — hidden storage

`hidage` is a peer-to-peer, pseudonymous, distributed backup system. The goal: back
up a file or folder by spreading **encrypted, fountain-coded shards** across a P2P
network instead of a central server. Each node contributes more local disk than it
consumes remotely (e.g. 2–4×), trading abundant local space for durable remote
storage, while a future reputation ledger tracks which hosts are reliable.

> **Status: Phase 1 (MVP) — local core.** This milestone implements the full data
> pipeline locally, with no networking yet. Shards are kept in a local
> content-addressed store. The networking, reputation ledger, and anonymity layers
> are planned for later phases (see [Roadmap](#roadmap)). The code is deliberately
> structured behind interfaces so those layers drop in without rework.

## How it works

```
file/folder ──pack──▶ blob ──encrypt──▶ ciphertext ──fountain-shard──▶ shards ──▶ store
                                                                                      │
restored file/folder ◀─unpack─ blob ◀─decrypt─ ciphertext ◀──fountain-decode── any N+ of M
```

1. **Pack** — a folder becomes a deterministic `tar` stream; a single file passes
   through unchanged.
2. **Encrypt** — a random per-backup data key (DEK) seals the blob with
   ChaCha20-Poly1305 in 64 KiB chunks (with truncation protection). The DEK is wrapped
   under a key derived from your passphrase via Argon2id. Hosts holding shards learn
   nothing about the content.
3. **Shard** — the ciphertext is expanded with an R10 Raptor **fountain code** into
   `M` shards. Any sufficiently large subset (`K` plus a small coding overhead)
   reconstructs the blob, in any order — like BitTorrent, but rateless.
4. **Store** — shards are written to a local content-addressed store keyed by SHA-256.
5. **Manifest** — a signed JSON "recipe" (`*.hmanifest`) records the crypto and
   fountain parameters, the wrapped DEK, and the shard list. **Keep this file** — it
   is what you need to restore. It is signed by your Ed25519 identity.

## Build

```sh
go build -o hidage ./cmd/hidage
```

## Usage

```sh
# One-time: create config dir (~/.config/hidage) and a node identity
hidage init

# Back up a folder with 3x redundancy; writes ./sampledir.hmanifest
hidage backup ./sampledir --redundancy 3

# Inspect a manifest and local shard availability
hidage info sampledir.hmanifest

# Check shard integrity / whether a restore is still possible
hidage verify sampledir.hmanifest

# Restore into ./out (recreates ./out/sampledir)
hidage restore sampledir.hmanifest ./out
```

The passphrase is read interactively. For scripting/tests, set `HIDAGE_PASSPHRASE`.

## End-to-end demo

This is the rateless property in action — drop a chunk of the shards and still recover:

```sh
go build -o hidage ./cmd/hidage
export HIDAGE_PASSPHRASE='correct horse battery staple'

hidage init
mkdir -p sampledir/sub && echo hello > sampledir/a.txt && head -c 200000 /dev/urandom > sampledir/big.bin
hidage backup ./sampledir --redundancy 3 --out s.hmanifest

# Delete half the shards from the store, then restore anyway
STORE="${XDG_CONFIG_HOME:-$HOME/.config}/hidage/store"
find "$STORE" -type f | head -6 | xargs rm
hidage restore s.hmanifest ./out
diff -r sampledir out/sampledir && echo "restored identically"
```

## Tests

```sh
go test ./...
```

Coverage includes the encryption round-trip (plus wrong-key/truncation/corruption
rejection), the fountain encode/decode from a random shard subset (and failure below
the threshold), deterministic archive pack/unpack, manifest sign/verify/tamper
detection, and the content-addressed store.

## Project layout

| Path | Responsibility |
|---|---|
| `cmd/hidage` | CLI commands (`init`, `backup`, `restore`, `info`, `verify`) |
| `internal/config` | XDG config/store path resolution |
| `internal/identity` | Ed25519 node identity (future libp2p peer key) |
| `internal/archive` | folder ↔ deterministic tar; single-file passthrough |
| `internal/crypto` | Argon2id KEK, DEK wrap/unwrap, chunked ChaCha20-Poly1305 |
| `internal/shard` | fountain encode/decode (wraps `google/gofountain`) |
| `internal/store` | `Store` interface + local content-addressed FS store |
| `internal/manifest` | signed recovery recipe |

## Known limitations (MVP)

- **No networking yet** — shards live only in the local store.
- **In-memory pipeline** — the whole blob and its shards are held in memory, so very
  large inputs are constrained by RAM. Streaming/block-wise sharding comes later.
- **Content-addressed dedup vs. small K** — for small inputs the fountain code uses
  few source symbols (`K` as low as 4), and distinct encoded symbols can occasionally
  share identical bytes, so they deduplicate to one stored blob. This reduces the true
  distinct-blob redundancy below the nominal `M`. Negligible for realistically sized
  inputs; revisit alongside RaptorQ.
- **Raptor (R10), not RaptorQ** — `gofountain` is pure-Go but implements RFC 5053
  Raptor, not the newer RFC 6330 RaptorQ. RaptorQ (e.g. via a cgo binding) is a future
  upgrade.

## Roadmap

- **Phase 2 — networking:** go-libp2p host keyed by the Ed25519 identity, Kademlia DHT
  for shard discovery (provider records), push/pull shard transfer. A libp2p-backed
  `Store` slots in behind the existing interface.
- **Phase 3 — reputation/ledger:** proof-of-storage audit challenges, signed receipts,
  and space accounting for the 2–4× contribution model.
- **Phase 4 — anonymity routing:** optional Tor / Veilid transport to hide IPs.
