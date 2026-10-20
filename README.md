# hidage — hidden storage

`hidage` is a peer-to-peer, pseudonymous, distributed backup system. The goal: back up a
file or folder by spreading **encrypted, erasure-coded shards** across a P2P network
instead of a central server: you store your friends' backups, they store yours, each
within the space you give each other, and audits check that they still hold it.

> **Status: networked + redundant.** The encrypted **versioned storage core**
> (content-addressed repository with incremental snapshots, history, diff, prune)
> distributes over **libp2p** between friends (direct, through a friend's relay, or hole-punched);
> a snapshot is **erasure-coded** and its shards spread across your friends' nodes (any K of M
> reconstruct, so it survives losing a minority); each friend keeps your data within the
> space they give you, renewals send only what changed, and **proof-of-storage audits**
> show whether they still hold it. A backup is recoverable on a fresh
> machine from just the **recovery phrase + one friend's contact card**.

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
| Settings | `~/.config/hidage/config.toml` | written with commented defaults by `init`: nickname, store, listen address, hosting quota/lease, redundancy |
| Keyring (root secret → master key + identity) | `~/.config/hidage/keyring.json` | one 32-byte root, wrapped under your passphrase; export it as a recovery phrase |
| Per-folder metadata (config, snapshots, DAG nodes) | `<folder>/.hidage/` | travels with the folder, like `.git` |
| Bulk chunk data | `~/.local/share/hidage/store` (or `store` in config, or `$HIDAGE_STORE`) | node-wide; one pooled copy per chunk, hard-linked per repo so identical chunks dedup across folders |

> **Want the real internals?** [`DESIGN.md`](DESIGN.md) details the key hierarchy, the
> deterministic AEAD scheme, the DAG node formats, snapshot signing/history, and the
> prune/GC algorithm — plus the threat model and what's deliberately out of scope.

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
hidage status                     # is this backup protected? snapshot age, published?, peers,
                                  #   fault tolerance, failing audits (non-zero exit if degraded)
hidage diff <idA> <idB>           # paths added / removed / modified between snapshots
hidage restore <id> ./out         # materialize a version into ./out
hidage forget <id>                # drop a snapshot record …
hidage prune                      # … then GC objects no snapshot references

hidage recover                    # on a new machine: rebuild the keyring from the phrase

hidage id                         # your key, addresses and contact card (no passphrase needed)
hidage friend invite              # print a single-use invite (valid 24h)
hidage friend add <invite>        # accept it: you become friends both ways
hidage friend add <card>          # or add a contact card (they must add yours too)
hidage friend set bob --give 200GiB   # change a friend's nickname or the space you give them
hidage friend ls                  # list friends; `friend rm <name>` removes one

# networking
hidage serve                      # run a node: serve objects, accept pushes from friends
                                  #   only (connections from anyone else are refused),
                                  #   within the space you give each; replaced data is kept in
                                  #   trash for a while, unrenewed backups expire
hidage publish                    # erasure-code the latest snapshot and spread the shards
                                  #   across every reachable friend (--redundancy, --min-peers
                                  #   default to config.toml)
hidage publish --peer <addr>      # …or push to a named peer instead
hidage net-list                   # list all folders you've published
hidage net-restore ./out --all    # recover from your friends: resolve the account,
                                  #   gather K-of-M shards, rebuild, restore (--repo <name>
                                  #   for one; on a fresh machine add --from <friend's card>)

# audits
hidage audit                      # challenge the friends holding this repo's shards to
                                  #   prove they still have them (results in status, friend ls)
```

- A tracked folder is one containing `.hidage/`; `snapshot` creates it on first use.
  Other commands **discover it by walking up from the current directory** (git-style),
  or pass `-C <dir>` to start elsewhere.
- The **bulk chunk store** defaults to `~/.local/share/hidage/store` (an existing
  `/opt/hidage/store` is still used); set `store` in `config.toml` or the `HIDAGE_STORE` env
  var to put it elsewhere, on a roomy disk excluded from your backups.
- **Settings** live in `~/.config/hidage/config.toml`; `serve` and `publish` take their
  listen address, lease, redundancy and minimum peer count from it unless a flag overrides.
- Snapshot IDs may be given as any unambiguous **prefix**.
- The passphrase (set at `init`) is read interactively; for scripting/tests set
  `HIDAGE_PASSPHRASE`.
- **Recovery:** `recovery-code` prints a 24-word BIP39 phrase encoding the root secret
  (master key + identity). Keep it offline — anyone with it can decrypt your backups, and
  it's the only way to get your key back if you lose this machine. `hidage recover` reads
  the phrase from stdin and rebuilds the keyring (under a new passphrase). With the phrase
  plus one friend's contact card (`--from`), `hidage net-restore` rebuilds the snapshots and files from the
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
reachability, the three stores (FS + hardlink cross-repo dedup and refcounted delete +
per-friend hosted sets: replacement, trash, lease expiry, quota by bytes held), the keyring (deterministic derivation, wrong-pass,
recovery-code roundtrip), and the repository end to end — snapshot/restore, `.hidage`
exclusion, incremental-delta accounting, history/diff, `--replace`, and forget + prune.
Networking: two-node object transfer, push and inbound connections gated by the friend list,
contact cards, a NATed node reached through a friend's relay, signed address records,
signed head push/resolve (owner-only, newest wins), proof-of-storage audits, push v2 (only missing objects sent, quota refusal, a friend can't
read another's data), the account manifest
(upsert/find/roundtrip), Reed-Solomon encode/decode (incl. decode from any K subset),
deterministic pack build + reassemble below and above the shard threshold, a
**cross-node recovery** test, and the **redundant-recovery** test (shards spread over 3
peers, one killed, full byte-identical recovery from the survivors).

## Project layout

| Path | Responsibility |
|---|---|
| `cmd/hidage` | CLI: snapshot/restore/diff/prune, recovery-code/recover, id/friend, serve/publish/net-restore/net-list, audit, status |
| `internal/config` | `config.toml` settings, config + data-store path resolution (`HIDAGE_STORE`) |
| `internal/keyring` | root secret (wrapped under passphrase) → master key + identity; BIP39 recovery code |
| `internal/identity` | Ed25519 keypair derived from the keyring (libp2p peer key) |
| `internal/friends` | friend list (key, nickname, addresses, quota) and `hidage1:` contact cards |
| `internal/hosting` | friends' data: per-friend sets, quota by bytes held, trash, lease expiry |
| `internal/crypto` | Argon2id KEK, key wrap/unwrap, deterministic content-addressed seal |
| `internal/store` | `Store`; `FSStore` (meta) + `HardlinkStore` (pooled, cross-repo dedup) + `KeyedStore` |
| `internal/chunk` | content-defined chunking (FastCDC) |
| `internal/dag` | Merkle DAG: chunk/file/tree nodes (meta store) routed apart from chunk data |
| `internal/repo` | versioned repository: `.hidage` layout, snapshots, restore, diff, prune, publish-set |
| `internal/account` | account manifest: one identity's index of all its network backups |
| `internal/shard` | Reed-Solomon erasure coding (`klauspost/reedsolomon`): any K of M shards reconstruct |
| `internal/pack` | group objects into packs, erasure-code them, Release descriptor for reassembly |
| `internal/p2p` | libp2p host (peer key = identity); object/push/audit/head/address protocols between friends, NAT traversal (relays, hole punching) |
| `internal/netstore` | network-backed object source (fetch from friends) for restoring |

## Known limitations

> **Security & incentives:** confidentiality and integrity are cryptographic and strong;
> hidage is built for backup **with friends**: you store shards only on peers you know
> and trust. See [`THREAT_MODEL.md`](THREAT_MODEL.md) for the full adversarial analysis.


- **Peer selection** — `publish` spreads to your reachable friends, or `--peer` names a
  host explicitly. There is no discovery of strangers' storage. Erasure coding only helps across enough distinct
  peers, so `--min-peers` guards against (and a single-peer publish warns about)
  configurations with no fault tolerance.
- **Reachability** — nodes behind NATs reach each other through a friend's relay (then
  hole punching), so at least one friend in the group needs a public address (a port
  forward, a small VPS running `serve`, or an overlay such as Tailscale/WireGuard).
  See `reachability` in `config.toml`.
- **Pseudonymous, not IP-anonymous** — peers are identified by public key, but their IP
  addresses are visible to peers they connect to (network-level anonymity is out of scope).
- **Single-writer** — snapshots assume one owner per repository; concurrent writers from
  multiple devices (conflict resolution) are not handled.
- **No at-rest forward secrecy** — a leaked user master key decrypts every snapshot in
  every repository it encrypted.
- **Data store needs one Unix filesystem** — cross-folder dedup uses hard links, so the
  pool and per-repo references must live on a single hard-link-capable filesystem.

## Roadmap

- **Phase 1 — local crypto/coding core.** ✅ Done. Client-side encryption, the
  content-addressed store, and the erasure-coding primitive (`internal/shard`).
- **Phase 2 — versioned storage core.** ✅ Done. The repository: content-defined chunking
  + Merkle DAG + signed snapshots, so updating/changing/removing files re-stores only the
  delta, with history and prune/GC.
- **Phase 3 — networking + trust + recovery.** ✅ Done. Recovery code (`recovery-code` /
  `recover`); a friend list (`friend …`, formerly a trust whitelist); a **libp2p** node (`serve`) keyed by the
  Ed25519 identity with object + push protocols (originally over a Kademlia DHT, since
  removed in favour of talking to friends directly); `publish`
  replicates a snapshot's objects (bytes, DAG nodes, chunks) to friends and posts a
  **signed head pointer**; `net-restore` rebuilds a backup from the network with only the
  recovery phrase + one friend's contact card.
- **Phase 4 — audits + redundancy.** ✅ Done. Proof-of-storage **audits** (challenge a
  peer to prove it still holds a shard, signed receipt), originally feeding reputation
  scores and a contribution-ratio ledger (since replaced by per-friend quotas); and a
  snapshot is **erasure-coded** (`internal/pack` + Reed-Solomon) into shards
  spread across peers, so it survives the loss of any minority of them.

> A paid storage service is **only an idea, not a planned feature.** The chosen
> primitives (signed pubkey identities + friend list, content-addressed verifiable
> shards, audit-backed health checks) are deliberately enough to make one *possible* later
> without any market-specific work in scope today.
