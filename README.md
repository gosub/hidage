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

A **repository** holds your data as encrypted, content-addressed objects plus a chain of
signed **snapshots**. Taking a snapshot stores only what changed since the last one:

```
files ─FastCDC chunk─▶ chunks ─encrypt─▶ Merkle DAG (file + tree nodes) ─▶ signed snapshot
                          └── an unchanged chunk keeps its hash ⇒ stored once, reused ──┘
```

1. **Chunk** — files are split with content-defined chunking (FastCDC), so editing or
   inserting bytes mid-file only changes nearby chunks; the rest keep their identity.
2. **Encrypt** — each chunk (and DAG node) is sealed with ChaCha20-Poly1305 under a
   per-repository master key, which is itself wrapped by an Argon2id key derived from
   your passphrase. Encryption is *deterministic* (key/nonce derived from the content
   under the master key), so identical chunks encrypt identically and dedup — without
   leaking chunk-equality across users.
3. **Build the Merkle DAG** — chunks → file nodes → directory (tree) nodes → a root, each
   content-addressed. An unchanged file or subtree keeps the same reference and is never
   re-stored.
4. **Snapshot** — an Ed25519-signed root chained to its parent. Re-snapshotting an
   unchanged tree stores **nothing**; changing one file stores only its new chunks plus
   the tree nodes on the path to the root. Removing a file is just a new snapshot that
   omits it; old snapshots stay restorable until you forget and prune them.

> **Want the real internals?** [`DESIGN.md`](DESIGN.md) details the key hierarchy, the
> deterministic AEAD scheme, the DAG node formats, snapshot signing/history, and the
> prune/GC algorithm — plus the threat model and what's deferred to later phases.

## Build

```sh
go build -o hidage ./cmd/hidage
```

## Usage

```sh
hidage init                       # one-time: create a node identity (signs snapshots)

hidage snapshot ./project         # snapshot a file/folder (creates the repo on first use)
hidage snapshots                  # list history, oldest first
hidage diff <idA> <idB>           # paths added / removed / modified between snapshots
hidage restore <id> ./out         # materialize a version into ./out
hidage forget <id>                # drop a snapshot record …
hidage prune                      # … then GC objects no snapshot references
```

- Snapshots live in a **default repository** at `~/.config/hidage/repo`. Use a different
  one with `--repo <dir>` on any command; it is created automatically on first
  `snapshot`.
- Snapshot IDs may be given as any unambiguous **prefix**.
- The passphrase is read interactively; for scripting/tests set `HIDAGE_PASSPHRASE`.

## End-to-end demo

Incremental snapshots in action — a one-byte change re-stores almost nothing:

```sh
go build -o hidage ./cmd/hidage
export HIDAGE_PASSPHRASE='correct horse battery staple'

hidage init
mkdir -p project/src
echo hello > project/a.txt
head -c 3000000 /dev/urandom > project/big.bin     # a large, unchanging file

hidage snapshot ./project                           # snapshot 1 (creates the repo)
echo goodbye > project/a.txt                        # edit one small file
hidage snapshot ./project                           # snapshot 2 stores only the delta

hidage snapshots
A=$(hidage snapshots | awk 'NR==2{print $1}')
hidage diff "$A" "$(hidage snapshots | awk 'NR==3{print $1}')"   # ~ project/a.txt

hidage restore "$A" ./out                           # older version restores intact
diff -r project/big.bin out/project/big.bin && echo "big.bin restored identically"
```

`big.bin`'s chunks are written once and reused across both snapshots; only `a.txt`'s new
chunk and the tree nodes up to the root are added the second time.

## Tests

```sh
go test ./...
```

Coverage includes: deterministic chunk encryption (determinism + wrong-key/corruption
rejection), content-defined chunk stability under insertion, DAG round-trip / diff /
reachability, the content-addressed store, and the repository end to end —
snapshot/restore, incremental-delta accounting, forget + prune, and history/diff. The
`shard` (fountain-coding) package is tested standalone, reserved for the distribution
phase.

## Project layout

| Path | Responsibility |
|---|---|
| `cmd/hidage` | CLI: `init`, `snapshot`, `snapshots`, `restore`, `diff`, `forget`, `prune` |
| `internal/config` | XDG config + default-repo path resolution |
| `internal/identity` | Ed25519 node identity (future libp2p peer key) |
| `internal/crypto` | Argon2id KEK, key wrap/unwrap, deterministic content-addressed seal |
| `internal/store` | `Store` interface + local content-addressed FS store |
| `internal/chunk` | content-defined chunking (FastCDC) |
| `internal/dag` | Merkle DAG: chunk/file/tree nodes, diff, reachability |
| `internal/repo` | versioned repository: snapshots, restore, diff, prune |
| `internal/shard` | fountain encode/decode (`google/gofountain`); reserved for Phase 3 distribution |

## Known limitations

- **No networking yet** — objects live only in the local repository; nothing is
  distributed. Fountain-coded redundancy across peers arrives with networking (Phase 3).
- **Pseudonymous, not IP-anonymous** — peers are identified by public key; IP-level
  anonymity is a later phase.
- **Single-writer** — snapshots assume one owner per repository; concurrent writers from
  multiple devices (conflict resolution) are not handled.
- **No at-rest forward secrecy** — a leaked repository master key decrypts every snapshot
  in that repository.

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
