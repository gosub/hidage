# hidage — hidden storage

`hidage` is a peer-to-peer, pseudonymous, distributed backup system. The goal: back up a
file or folder by spreading **encrypted, fountain-coded shards** across a P2P network
instead of a central server. Each node contributes more local disk than it consumes
remotely (e.g. 2–4×), trading abundant local space for durable remote storage, while a
reputation ledger tracks which hosts are reliable.

> **Status: local, no networking yet.** What works today is the encrypted, **versioned
> storage core**: a content-addressed repository with incremental snapshots, history,
> diff, and pruning (git/restic-style). Distributing shards over a P2P network — where
> the fountain coding and reputation ledger come in — is the next phase (see
> [Roadmap](#roadmap)). The code is structured behind interfaces (notably `Store`) so
> those layers drop in without rework.

## How it works

You back up a folder by **snapshotting** it. Like git, each tracked folder gets a
`.hidage/` directory holding its metadata; unlike git, the bulk file data goes to a
separate node-wide store, and the encryption key is shared across all your folders.

```
files ─FastCDC chunk─▶ chunks ─encrypt─▶ Merkle DAG (file + tree nodes) ─▶ signed snapshot
                          └── an unchanged chunk keeps its hash ⇒ stored once, reused ──┘
```

1. **Chunk** — files are split with content-defined chunking (FastCDC), so editing or
   inserting bytes mid-file only changes nearby chunks; the rest keep their identity.
2. **Encrypt** — each chunk (and DAG node) is sealed with ChaCha20-Poly1305 under your
   **user-level master key** (one per user, wrapped by an Argon2id key from your
   passphrase). Encryption is *deterministic* (key/nonce derived from the content under
   the master key), so identical chunks encrypt identically and **deduplicate — even
   across different folders** — without leaking chunk-equality to other users.
3. **Build the Merkle DAG** — chunks → file nodes → directory (tree) nodes → a root, each
   content-addressed. An unchanged file or subtree keeps the same reference and is never
   re-stored.
4. **Snapshot** — an Ed25519-signed root chained to its parent. Re-snapshotting an
   unchanged tree stores **nothing**; changing one file stores only its new chunks plus
   the tree nodes on the path to the root. Old snapshots stay restorable until you forget
   and prune them (or use `snapshot --replace` to supersede the previous one in one step).

**Where things live:**

| What | Where | Notes |
|---|---|---|
| Node identity + master key | `~/.config/hidage/` | tiny; the key is wrapped under your passphrase |
| Per-folder metadata (config, snapshots, DAG nodes) | `<folder>/.hidage/` | travels with the folder, like `.git` |
| Bulk chunk data | `$HIDAGE_STORE` or `/opt/hidage/store` | node-wide, outside `$HOME`; one pooled copy per chunk, hard-linked per repo so identical chunks dedup across folders |

> **Want the real internals?** [`DESIGN.md`](DESIGN.md) details the key hierarchy, the
> deterministic AEAD scheme, the DAG node formats, snapshot signing/history, and the
> prune/GC algorithm — plus the threat model and what's deferred to later phases.

## Build

```sh
go build -o hidage ./cmd/hidage
```

## Usage

```sh
hidage init                       # one-time: create identity + user encryption key

hidage snapshot ./project         # snapshot a folder (creates ./project/.hidage on first use)
hidage snapshot --replace ./project   # … superseding the previous snapshot (no history kept)
cd project && hidage snapshots    # run inside a tracked folder; commands discover .hidage
hidage diff <idA> <idB>           # paths added / removed / modified between snapshots
hidage restore <id> ./out         # materialize a version into ./out
hidage forget <id>                # drop a snapshot record …
hidage prune                      # … then GC objects no snapshot references
```

- A tracked folder is one containing `.hidage/`; `snapshot` creates it on first use.
  Other commands **discover it by walking up from the current directory** (git-style),
  or pass `-C <dir>` to start elsewhere.
- The **bulk chunk store** defaults to `/opt/hidage/store`; override with the
  `HIDAGE_STORE` env var (create it once and make it writable — `hidage` prints the exact
  commands if it can't).
- Snapshot IDs may be given as any unambiguous **prefix**.
- The passphrase (set at `init`) is read interactively; for scripting/tests set
  `HIDAGE_PASSPHRASE`.

## End-to-end demo

Incremental snapshots in action — a one-byte change re-stores almost nothing:

```sh
go build -o hidage ./cmd/hidage
export HIDAGE_PASSPHRASE='correct horse battery staple'
export HIDAGE_STORE="$PWD/.hidage-data"        # a writable chunk store for the demo

hidage init
mkdir -p project
echo hello > project/a.txt
head -c 3000000 /dev/urandom > project/big.bin  # a large, unchanging file
hidage snapshot ./project                        # snapshot 1 (creates ./project/.hidage)

echo goodbye > project/a.txt                     # edit one small file
cd project
hidage snapshot                                  # snapshot 2 (discovers .hidage); stores only the delta

hidage snapshots
A=$(hidage snapshots | awk 'NR==2{print $1}')
hidage diff "$A" "$(hidage snapshots | awk 'NR==3{print $1}')"   # ~ project/a.txt
hidage restore "$A" /tmp/out                      # older version restores intact
diff /tmp/out/project/big.bin big.bin && echo "big.bin restored identically"
```

`big.bin`'s chunks are written once and reused across both snapshots (and would also be
reused if the same file appeared in a *different* tracked folder); only `a.txt`'s new
chunk and the tree nodes up to the root are added the second time.

## Tests

```sh
go test ./...
```

Coverage includes: deterministic chunk encryption (determinism + wrong-key/corruption
rejection), content-defined chunk stability under insertion, DAG round-trip / diff /
reachability, both stores (FS + hardlink cross-repo dedup and refcounted delete), the
user-level key (init/load/wrong-pass), and the repository end to end — snapshot/restore,
`.hidage` exclusion, incremental-delta accounting, history/diff, `--replace`, and
forget + prune. The `shard` (fountain-coding) package is tested standalone, reserved for
the distribution phase.

## Project layout

| Path | Responsibility |
|---|---|
| `cmd/hidage` | CLI: `init`, `snapshot`, `snapshots`, `restore`, `diff`, `forget`, `prune` |
| `internal/config` | config + data-store path resolution (`HIDAGE_STORE`) |
| `internal/identity` | Ed25519 node identity (future libp2p peer key) |
| `internal/userkey` | user-level encryption key, wrapped under the passphrase |
| `internal/crypto` | Argon2id KEK, key wrap/unwrap, deterministic content-addressed seal |
| `internal/store` | `Store` interface; `FSStore` (meta) + `HardlinkStore` (pooled, cross-repo-dedup data) |
| `internal/chunk` | content-defined chunking (FastCDC) |
| `internal/dag` | Merkle DAG: chunk/file/tree nodes (meta store) routed apart from chunk data |
| `internal/repo` | versioned repository: `.hidage` layout, snapshots, restore, diff, prune |
| `internal/shard` | fountain encode/decode (`google/gofountain`); reserved for Phase 3 distribution |

## Known limitations

- **No networking yet** — objects live only in the local stores; nothing is distributed.
  Fountain-coded redundancy across peers arrives with networking (Phase 3).
- **Pseudonymous, not IP-anonymous** — peers are identified by public key; IP-level
  anonymity is a later phase.
- **Single-writer** — snapshots assume one owner per repository; concurrent writers from
  multiple devices (conflict resolution) are not handled.
- **No at-rest forward secrecy** — a leaked user master key decrypts every snapshot in
  every repository it encrypted.
- **Data store needs one Unix filesystem** — cross-folder dedup uses hard links, so the
  pool and per-repo references must live on a single hard-link-capable filesystem.

## Roadmap

- **Phase 1 — local crypto/coding core.** ✅ Done. Client-side encryption, the
  content-addressed store, and the fountain-coding primitive (`internal/shard`).
- **Phase 2 — versioned storage core.** ✅ Done. The repository: content-defined chunking
  + Merkle DAG + signed snapshots, so updating/changing/removing files re-stores only the
  delta, with history and prune/GC.
- **Phase 3 — networking + trust.** go-libp2p host keyed by the Ed25519 identity,
  Kademlia DHT for discovery, and shard transfer behind the existing `Store` interface
  (objects grouped into packs, each fountain-sharded for redundant distribution); plus a
  **trust/whitelist policy** letting a node bypass reputation for keys it explicitly
  trusts (friend-to-friend, à la Syncthing pairing) or block.
- **Phase 4 — reputation.** Proof-of-storage audit challenges, signed receipts, and space
  accounting for the 2–4× contribution model.
- **Phase 5 — anonymity routing.** Optional Tor / Veilid transport to hide IPs.

> A paid storage service is **only an idea, not a planned feature.** The chosen
> primitives (signed pubkey identities + trust policy, content-addressed verifiable
> shards, audit-backed reputation) are deliberately enough to make one *possible* later
> without any market-specific work in scope today.
