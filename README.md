# hidage — hidden storage

`hidage` is a peer-to-peer, pseudonymous, distributed backup system. The goal: back
up a file or folder by spreading **encrypted, fountain-coded shards** across a P2P
network instead of a central server. Each node contributes more local disk than it
consumes remotely (e.g. 2–4×), trading abundant local space for durable remote
storage, while a future reputation ledger tracks which hosts are reliable.

> **Status: local, no networking yet.** Two storage models are implemented locally:
> the **Phase 1 one-shot** pipeline (`backup`/`restore`, below) and the **Phase 2
> versioned repository** (`repo`, [below](#versioned-storage-repo-phase-2)) with
> incremental snapshots, history, and pruning. Networking, the reputation ledger, and
> anonymity are later phases (see [Roadmap](#roadmap)); the code is structured behind
> interfaces so they drop in without rework.

## How it works

Everything is encrypted client-side under a key derived from your passphrase, then kept
in a content-addressed store (blobs keyed by SHA-256). hidage offers two storage models
on top of those shared primitives:

**One-shot backup** (`backup`) — freeze a file/folder and spread it redundantly:

```
file/folder ─encrypt─▶ ciphertext ─fountain-shard─▶ M shards ─▶ store
                                                                   │
       file/folder ◀──── decrypt ◀──── fountain-decode ◀── any K(+ε) of the M shards
```

The ciphertext is expanded with an R10 Raptor **fountain code** into `M` shards; any
sufficient subset (`K` plus a little overhead) reconstructs it, in any order — like
BitTorrent, but rateless. A signed `*.hmanifest` is the recovery recipe.

**Versioned repository** (`repo`) — update/change/remove files efficiently, with history:

```
files ─FastCDC chunk─▶ chunks ─▶ Merkle DAG (file + tree nodes) ─▶ signed snapshot
                          └── unchanged chunks keep their hash ⇒ stored once, reused ──┘
```

Files are split by content (so a mid-file edit only touches nearby chunks), assembled
into a Merkle DAG, and captured as a signed snapshot chained to its parent. Re-storing an
unchanged tree writes **nothing**; changing one file stores only its new chunks plus the
tree nodes on the path to the root. Per-repo deterministic encryption makes identical
chunks dedup without leaking chunk-equality across users.

> **Want the real internals?** [`DESIGN.md`](DESIGN.md) details the key hierarchy, the
> chunked + deterministic AEAD schemes, the DAG node formats, snapshot signing/history,
> and the prune/GC algorithm — plus the threat model and what's deferred to later phases.

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

## Versioned storage (`repo`, Phase 2)

The one-shot `backup` above re-shards everything when a single file changes. The `repo`
command instead keeps a **versioned repository** that re-stores only what changed —
content-defined chunking + a Merkle DAG + signed snapshots, git/restic-style. See
[`DESIGN.md`](DESIGN.md#model-2--versioned-repository-phase-2) for the mechanism; the
commands:

```sh
hidage init                              # node identity (signs snapshots)
hidage repo init ./vault                 # create a repository

hidage repo snapshot ./vault ./project   # take a snapshot (incremental)
hidage repo snapshots ./vault            # list history
hidage repo diff ./vault <idA> <idB>     # changed/added/removed paths
hidage repo restore ./vault <id> ./out   # materialize a version
hidage repo forget ./vault <id>          # drop a snapshot record …
hidage repo prune ./vault                # … then GC objects nothing references
```

Snapshot IDs may be given as any unambiguous prefix. Re-snapshotting an unchanged tree
stores **0** new objects; changing one small file stores only that file's new chunk plus
the handful of tree nodes on the path to the root, even if the repo holds large
unchanged files alongside it.

## Tests

```sh
go test ./...
```

Coverage includes: Phase 1 — encryption round-trip (wrong-key/truncation/corruption
rejection), fountain encode/decode from a random shard subset (and sub-threshold
failure), deterministic archive pack/unpack, manifest sign/verify/tamper, the
content-addressed store. Phase 2 — deterministic chunk encryption, content-defined
chunk stability under insertion, DAG round-trip/diff/reachability, and repository
snapshot/restore/incremental-delta/forget+prune/history.

## Project layout

| Path | Responsibility |
|---|---|
| `cmd/hidage` | CLI commands (`init`, `backup`, `restore`, `info`, `verify`, `repo …`) |
| `internal/config` | XDG config/store path resolution |
| `internal/identity` | Ed25519 node identity (future libp2p peer key) |
| `internal/archive` | folder ↔ deterministic tar; single-file passthrough |
| `internal/crypto` | Argon2id KEK, DEK wrap/unwrap, chunked AEAD + deterministic content-addressed seal |
| `internal/shard` | fountain encode/decode (wraps `google/gofountain`) |
| `internal/store` | `Store` interface + local content-addressed FS store |
| `internal/manifest` | signed recovery recipe (Phase 1 one-shot) |
| `internal/chunk` | content-defined chunking (FastCDC) |
| `internal/dag` | Merkle DAG: chunk/file/tree nodes, diff, reachability |
| `internal/repo` | versioned repository: snapshots, restore, diff, prune |

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

- **Phase 1 — local core.** ✅ Done. The monolithic pipeline above: pack → encrypt →
  fountain-shard → store, restore from any sufficient subset, signed manifest.
- **Phase 2 — versioned storage core.** ✅ Done. The `repo` command: **content-defined
  chunking + a Merkle DAG + signed snapshots**, so updating, changing, or removing files
  re-stores only the delta (git/restic-style), with history kept. Per-repo deterministic
  encryption gives content-addressed dedup within your own data without leaking
  chunk-equality across users. (Fountain re-sharding of objects for redundant
  *distribution* lands with networking in Phase 3, where it actually matters.)
- **Phase 3 — networking + trust.** go-libp2p host keyed by the Ed25519 identity,
  Kademlia DHT for discovery, pack/shard transfer behind the existing `Store` interface;
  plus a **trust/whitelist policy** letting a node bypass reputation for keys it
  explicitly trusts (friend-to-friend, à la Syncthing pairing) or block.
- **Phase 4 — reputation.** Proof-of-storage audit challenges, signed receipts, and
  space accounting for the 2–4× contribution model.
- **Phase 5 — anonymity routing.** Optional Tor / Veilid transport to hide IPs.

> A paid storage service is **only an idea, not a planned feature.** The chosen
> primitives (signed pubkey identities + trust policy, content-addressed verifiable
> shards, audit-backed reputation) are deliberately enough to make one *possible* later
> without any market-specific work in scope today.
