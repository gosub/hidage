# hidage — hidden storage

`hidage` is backup **with your friends**: your folders are encrypted, versioned and
erasure-coded, and the pieces are spread across your friends' machines instead of a
cloud service. You store their backups, they store yours, each within the space you give
each other, and audits check that they still hold it. Lose your laptop, and the 24-word
recovery phrase plus one friend's contact card get everything back.

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
   the tree nodes on the path to the root. Old snapshots stay restorable until you
   `forget` them (or use `backup --replace` to supersede the previous one in one step).

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

## Getting started

```sh
hidage init                       # once per machine: keyring, config.toml, data store
hidage id --kit                   # print your recovery kit; keep it offline

hidage friend invite              # send the printed invite to a friend, privately;
                                  #   they run `hidage friend add <invite>` and you are
                                  #   friends both ways (they need a running daemon too)
hidage backup ~/Documents         # snapshot and send to your friends; the folder is added
                                  #   to the ones the daemon keeps backed up
hidage daemon --install           # run the daemon at login (systemd/launchd), then set
                                  #   passphrase_command in config.toml for unattended backups
```

## The nine commands

```sh
hidage init [--recover]           # set up this machine; --recover: from your phrase (stdin)
hidage id [--card | --kit]        # your key, addresses and contact card (no passphrase);
                                  #   --kit adds the recovery phrase and friends' cards
hidage friend invite              # single-use invite, valid 24h (--give, --name, --valid)
hidage friend add <invite|card>   # accept an invite (mutual) or add a contact card
hidage friend set bob --give 200GiB   # change a nickname or the space you give
hidage friend ls [-v]             # space both ways, last audit, last seen
hidage friend rm bob [--purge]    # remove (and delete what you hold for them)

hidage backup [folder]            # snapshot + send what changed to every reachable friend
                                  #   (--local: snapshot only; --once: don't schedule it)
hidage restore --to ~/out         # the latest snapshot of this folder
hidage restore 3f2a sub/x.txt --to .   # one file of an older snapshot
hidage restore --list [snap] [path]    # browse a snapshot
hidage restore --friends --to ~/out    # from your friends (new machine: --from <card>)
hidage log                        # history, newest first, marking what friends hold
hidage log --diff a,b             # what changed between two snapshots
hidage log --friends              # every backup your friends hold for you
hidage forget <snap>...           # drop snapshots and free the space only they used
hidage status [--audit]           # is this folder protected? freshness, friends' health,
                                  #   fault tolerance, lease; non-zero exit when degraded
hidage daemon [--install]         # scheduled backups + host and relay for friends
```

- Commands run inside a backed-up folder **discover its `.hidage/`** by walking up from
  the current directory (git-style), or pass `-C <dir>`. Snapshot ids may be shortened to
  any unambiguous prefix, and `latest` names the newest.
- **Settings** live in `~/.config/hidage/config.toml`, written with comments by `init`:
  the folders the daemon backs up and how often, redundancy, the space you give friends
  by default, how long friends' data is kept, network addresses and reachability.
- The **bulk chunk store** defaults to `~/.local/share/hidage/store` (an existing
  `/opt/hidage/store` is still used); set `store` in `config.toml` or `HIDAGE_STORE` to
  put it on a roomy disk excluded from your backups.
- The **passphrase** comes from `HIDAGE_PASSPHRASE`, then `passphrase_command` (e.g.
  `systemd-creds`, `pass`), then the terminal. A daemon started with none of these runs
  **locked**: it hosts and relays for friends from a cached identity key (`identity.key`,
  which can't decrypt backups) but takes no backups, and `status` says so.
- **History:** `[retention]` in `config.toml` (default: the last 3 versions plus one per day
  for a week and one per week for a month) decides which versions friends keep and which
  the daemon keeps locally; `forget --retention` applies it now.
- **Recovery:** `id --kit` prints the 24-word phrase (your root secret: anyone with it can
  decrypt your backups) and your friends' cards. On a new machine, `init --recover` and
  `restore --friends --from <one card>` bring back your files, your friend list (kept
  encrypted in your backup) and each folder as a repository, so `backup` resumes where you
  left off and only renews what friends already hold.

## End-to-end demo

Incremental snapshots in action, on one machine — a one-byte change re-stores almost
nothing:

```sh
go build -o hidage ./cmd/hidage
export HIDAGE_PASSPHRASE='correct horse battery staple'

hidage init
mkdir -p project
echo hello > project/a.txt
head -c 3000000 /dev/urandom > project/big.bin  # a large, unchanging file
hidage backup --local project                    # snapshot 1 (creates project/.hidage)

echo goodbye > project/a.txt                     # edit one small file
cd project
hidage backup --local                            # snapshot 2: stores only the delta

hidage log
hidage log --diff "$(hidage log | awk 'NR==2{print $1}')","$(hidage log | awk 'NR==1{print $1}')"   # ~ project/a.txt
hidage restore "$(hidage log | awk 'NR==2{print $1}')" --to /tmp/out   # the older version
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
| `cmd/hidage` | CLI: init, id, friend, backup, restore, log, forget, status, daemon |
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


- **Peer selection** — `backup` spreads to your reachable friends, or `--peer` names a
  host explicitly. There is no discovery of strangers' storage. Erasure coding only helps across enough distinct
  peers, so `--min-peers` guards against (and a single-peer publish warns about)
  configurations with no fault tolerance.
- **Reachability** — nodes behind NATs reach each other through a friend's relay (then
  hole punching), so at least one friend in the group needs a public address (a port
  forward, a small VPS running `daemon`, or an overlay such as Tailscale/WireGuard).
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
  `recover`); a friend list (`friend …`, formerly a trust whitelist); a **libp2p** node (`daemon`) keyed by the
  Ed25519 identity with object + push protocols (originally over a Kademlia DHT, since
  removed in favour of talking to friends directly); publishing
  replicates a snapshot's objects (bytes, DAG nodes, chunks) to friends and posts a
  **signed head pointer**; restoring from friends rebuilds a backup from the network with only the
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
