# hidage — hidden storage

`hidage` is a peer-to-peer, pseudonymous, distributed backup system. The goal: back up a
file or folder by spreading **encrypted, fountain-coded shards** across a P2P network
instead of a central server. Each node contributes more local disk than it consumes
remotely (e.g. 2–4×), trading abundant local space for durable remote storage, while a
reputation ledger tracks which hosts are reliable.

> **Status: networked.** The encrypted **versioned storage core** (content-addressed
> repository with incremental snapshots, history, diff, prune) now distributes over a
> **libp2p P2P network**: nodes discover objects via a Kademlia DHT, replicate a
> snapshot's objects to trusted peers, and publish a signed head pointer — so a backup is
> recoverable on a fresh machine from just the **recovery phrase + one bootstrap peer**.
> Still to come (Phase 4+): a reputation ledger, fountain-coded redundancy for
> distribution, and IP-level anonymity (see [Roadmap](#roadmap)).

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
| Keyring (root secret → master key + identity) | `~/.config/hidage/keyring.json` | one 32-byte root, wrapped under your passphrase; export it as a recovery phrase |
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
hidage init                       # one-time: create the keyring (master key + identity)
hidage recovery-code              # print the 24-word phrase — WRITE IT DOWN

hidage snapshot ./project         # snapshot a folder (creates ./project/.hidage on first use)
hidage snapshot --replace ./project   # … superseding the previous snapshot (no history kept)
cd project && hidage snapshots    # run inside a tracked folder; commands discover .hidage
hidage diff <idA> <idB>           # paths added / removed / modified between snapshots
hidage restore <id> ./out         # materialize a version into ./out
hidage forget <id>                # drop a snapshot record …
hidage prune                      # … then GC objects no snapshot references

hidage recover                    # on a new machine: rebuild the keyring from the phrase

hidage trust add <pubkey>         # trust a peer's key (bypass reputation, friend-to-friend)
hidage trust block <pubkey>       # block a peer's key (never interact)
hidage trust list                 # show trusted / blocked keys

# networking (Phase 3)
hidage serve --listen /ip4/0.0.0.0/tcp/4001   # run a node: join the DHT, serve objects,
                                              #   accept pushes from non-blocked peers
hidage publish --peer <addr> --bootstrap <addr>   # push the latest snapshot's objects to
                                                  #   storage peers + publish a signed head pointer
hidage net-restore ./out --bootstrap <addr>   # recover from the network: resolve your head,
                                              #   pull the snapshot + objects, restore
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
- **Recovery:** `recovery-code` prints a 24-word BIP39 phrase encoding the root secret
  (master key + identity). Keep it offline — anyone with it can decrypt your backups, and
  it's the only way to get your key back if you lose this machine. `hidage recover` reads
  the phrase from stdin and rebuilds the keyring (under a new passphrase). With the phrase
  plus a bootstrap peer, `hidage net-restore` rebuilds the snapshots and files from the
  network — no local `.hidage` or chunk store needed.

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
keyring (deterministic derivation, wrong-pass, recovery-code roundtrip), and the
repository end to end — snapshot/restore,
`.hidage` exclusion, incremental-delta accounting, history/diff, `--replace`, and
forget + prune. Networking: two-node object transfer, DHT provide/discover, push gated by
trust, signed head publish/resolve, and a full **cross-node recovery** integration test
(owner publishes to a storage peer, leaves, a fresh node restores everything from the
network). The `shard` (fountain-coding) package is tested standalone, reserved for
redundant distribution.

## Project layout

| Path | Responsibility |
|---|---|
| `cmd/hidage` | CLI: `init`, `snapshot`, `snapshots`, `restore`, `diff`, `forget`, `prune`, `recovery-code`, `recover` |
| `internal/config` | config + data-store path resolution (`HIDAGE_STORE`) |
| `internal/keyring` | root secret (wrapped under passphrase) → master key + identity; BIP39 recovery code |
| `internal/identity` | Ed25519 keypair derived from the keyring (future libp2p peer key) |
| `internal/trust` | peer trust policy: whitelist / blocklist, consulted before reputation |
| `internal/crypto` | Argon2id KEK, key wrap/unwrap, deterministic content-addressed seal |
| `internal/store` | `Store` interface; `FSStore` (meta) + `HardlinkStore` (pooled, cross-repo-dedup data) |
| `internal/chunk` | content-defined chunking (FastCDC) |
| `internal/dag` | Merkle DAG: chunk/file/tree nodes (meta store) routed apart from chunk data |
| `internal/repo` | versioned repository: `.hidage` layout, snapshots, restore, diff, prune |
| `internal/p2p` | libp2p host (peer key = identity), object/push protocols, Kademlia DHT, signed head pointers |
| `internal/netstore` | network-backed object source (DHT discover + fetch) for restoring from peers |
| `internal/shard` | fountain encode/decode (`google/gofountain`); reserved for redundant distribution |

## Known limitations

- **No automatic replication/redundancy yet** — `publish` pushes to the storage peers you
  name, and data is fetched from whoever has it; choosing peers automatically and
  fountain-coded redundancy (surviving the loss of a minority of peers) come with the
  reputation phase.
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
- **Phase 3 — networking + trust + recovery.** ✅ Done. Recovery code (`recovery-code` /
  `recover`); trust/whitelist policy (`trust …`); a **libp2p** node (`serve`) keyed by the
  Ed25519 identity with object + push protocols and a **Kademlia DHT**; `publish`
  replicates a snapshot's objects (bytes, DAG nodes, chunks) to trusted peers and posts a
  **signed head pointer**; `net-restore` rebuilds a backup from the network with only the
  recovery phrase + a bootstrap peer. (Objects are distributed directly; fountain-coded
  *redundancy* across many peers is folded in with the reputation phase.)
- **Phase 4 — reputation + redundancy.** Proof-of-storage audit challenges, signed
  receipts, space accounting for the 2–4× model; objects grouped into packs and
  fountain-sharded (`internal/shard`) so data survives the loss of any minority of peers.
- **Phase 5 — anonymity routing.** Optional Tor / Veilid transport to hide IPs.

> A paid storage service is **only an idea, not a planned feature.** The chosen
> primitives (signed pubkey identities + trust policy, content-addressed verifiable
> shards, audit-backed reputation) are deliberately enough to make one *possible* later
> without any market-specific work in scope today.
