# hidage — Design & Internals

This document is the technical reference for how hidage stores data. For a quick
conceptual overview and CLI usage, see the [README](README.md). Sections marked
*(planned)* are not implemented yet.

hidage stores data as a **versioned repository**: encrypted, content-addressed objects
plus a chain of signed snapshots. Content-defined chunking and a Merkle DAG mean an
update re-stores only what changed; history is kept and old versions stay restorable.

---

## Shared primitives

### Key hierarchy (`internal/keyring`, `internal/crypto`, `internal/identity`)
Everything derives from one secret:

- **Root** — a single random 32-byte secret per user, generated at `hidage init`,
  wrapped under a passphrase **KEK** (Argon2id; defaults: time=3, memory=64 MiB, threads=4,
  16-byte salt) with ChaCha20-Poly1305 (`nonce || ciphertext`) and stored at
  `~/.config/hidage/keyring.json`. Only the wrapped form is written; a wrong passphrase
  fails the AEAD tag.
- **Master key** = `HKDF-SHA256(root, "hidage/master-key/v1", 32)` — encrypts all
  repository objects. Being per-user, identical chunks encrypt identically **across all
  your folders**, so the shared data store deduplicates them globally.
- **Identity** = Ed25519 keypair from `HKDF-SHA256(root, "hidage/identity/v1", 32)`. The
  public key is the node's pseudonymous identity (and future libp2p peer key); the private
  key signs snapshots. It is *derived*, not stored separately.
- **Recovery code** — the root encodes to a 24-word **BIP39** mnemonic (`hidage
  recovery-code`). Because both the master key and the identity derive from the root, this
  single phrase backs up the entire node's secrets; `hidage recover` rebuilds the keyring
  from it on a fresh machine (under a new passphrase).

### Content-addressed stores (`internal/store`)
A `Store` is a blob map keyed by the hex SHA-256 of its contents; `Put` is idempotent.
hidage uses **two** implementations:

- `FSStore` — writes `<root>/<id[:2]>/<id>` (mode 0600). Used for the small per-folder
  *meta* store (DAG nodes) in `.hidage/objects/`.
- `HardlinkStore` — the node-wide *data* store for chunks. One canonical copy of each
  blob lives in `pool/<id>`; each repository hard-links it into its own
  `refs/<repoID>/<id>` namespace. The filesystem's link count is therefore a
  cross-repository reference count: a repo prunes by unlinking only its own refs, and a
  blob's disk space is reclaimed only when the **last** repo unlinks it (the pool link is
  the sole remaining one). This deduplicates identical chunks across every folder while
  keeping prune safe and registry-free. It requires the pool and refs to share one Unix
  filesystem (hard links can't span filesystems).

The shared `Put/Get/Has/Delete/List` interface is also the seam a libp2p-backed network
store will implement in Phase 3.

---

## The repository (`internal/repo`)

Like git, each tracked folder carries its own metadata in a `.hidage/` directory; unlike
git, the bulk chunk data is externalized so it never bloats the folder (or a backup of
it):

```
<folder>/.hidage/
  config.json          # {version, repoID} — no key material (the key is user-level)
  objects/<id>         # meta store: encrypted DAG nodes (file/tree), small
  snapshots/<id>.json  # one signed snapshot root per version

<data store>  (default ~/.local/share/hidage/store)
  pool/<id>            # one canonical encrypted copy of each chunk (the bulk)
  refs/<repoID>/<id>   # this repo's hard links into the pool — its reference set
```

`Init` writes `.hidage/` and its marker config; `Open` reads it. Both take the user
master key (loaded once via `internal/keyring`) and the data-store path. `Find` locates a
repository by walking up from the current directory, so commands work git-style from
anywhere inside a tracked folder; `snapshot` auto-creates `.hidage/` on first use. The
data store path comes from `$HIDAGE_STORE`, else `store` in `config.toml`, else an existing
legacy `/opt/hidage/store`, else `$XDG_DATA_HOME/hidage/store`; a
permission failure there yields actionable setup guidance.

### Content-defined chunking (`internal/chunk`)
Files are split with **FastCDC** (`github.com/jotfs/fastcdc-go`, average 1 MiB, min 256
KiB, max 4 MiB, seed 0). Because boundaries are chosen from a rolling hash of the
*content*, inserting or removing bytes in the middle of a file only disturbs the chunks
around the edit — the rest realign and keep their identities. This is what makes
incremental snapshots cheap. The same input always chunks identically.

### The Merkle DAG (`internal/dag`)
Every object is content-addressed and referenced by a **`Ref`**:

```
Ref { Fp []byte; CipherID string; Size int64 }
```

- `Fp` — the deterministic-encryption fingerprint, needed (with the master key) to
  decrypt the object.
- `CipherID` — the object's address in the store (SHA-256 of its ciphertext).

Three object kinds, each serialized to JSON, deterministically encrypted, then stored:

```
chunk          raw file bytes (a leaf)
FileNode  { type:"file", mode, size, chunks: [Ref, …] }          ordered chunk refs
TreeNode  { type:"tree", entries: [{name, kind, mode, ref}, …] } name-sorted dir
```

The `dag.Builder`/`Loader` route by kind: chunks (the bulk) go to the node-wide **data**
store, while file and tree nodes (the small structure) go to the per-folder **meta** store
in `.hidage/objects/`. This is what lets the recipe travel with the folder while the heavy
data stays external.

A directory's `TreeNode` references child trees and files by `Ref`; a snapshot references
a single root `TreeNode`. Because a `Ref` is determined by content, an unchanged file or
subtree has the **same `Ref` across snapshots** and is never re-stored — exactly git's
blob/tree model:

```
snapshot.root ─▶ TreeNode(project)
                  ├─ a.txt      ─▶ FileNode ─▶ [chunk…]
                  └─ src        ─▶ TreeNode ─▶ b.txt ─▶ FileNode ─▶ [chunk…]
```

### Deterministic encryption & dedup (`internal/crypto`, `SealDeterministic`)
Content addressing requires that the same plaintext always produces the same stored
bytes. A random nonce would break that, so chunk/node encryption derives everything from
the plaintext under the secret master key:

```
fp    = HMAC-SHA256(masterKey, plaintext)               # the fingerprint
key   = HMAC-SHA256(masterKey, "hidage-det-key-v1"  || fp)[:32]
nonce = HMAC-SHA256(masterKey, "hidage-det-nonce-v1"|| fp)[:12]
ct    = ChaCha20-Poly1305(key, nonce).Seal(plaintext)
```

Identical plaintext ⇒ identical `fp` ⇒ identical `key/nonce` ⇒ identical ciphertext ⇒
identical `CipherID`, so `store.Put` dedups it. `fp` is stored inside the (encrypted)
parent node; on restore, `OpenDeterministic` re-derives `key/nonce` from `masterKey +
fp`, decrypts, and verifies the recovered plaintext re-hashes to `fp` (rejecting any
swapped `fp`/ciphertext pairing).

**Why HMAC under the master key rather than a bare `SHA-256(plaintext)`?** A bare
plaintext hash would let anyone who *guesses* a chunk confirm its presence (a convergent-
encryption confirmation attack). Keying the fingerprint with the user's secret means dedup
works **across all the owner's folders** (one user key) but leaks no chunk-equality to
other users.

### Snapshots & history
A snapshot is a signed root:

```
Snapshot { version, id, createdAt, parent, source, root: Ref, ownerPubKey, signature }
```

- `signature` — Ed25519 over the canonical payload (all fields except `id` and
  `signature`).
- `id` — hex SHA-256 of that same payload (content-addressed; any unambiguous prefix
  resolves it on the CLI).
- `parent` — the previous snapshot's `id`, forming a git-like chain. **Update** = a new
  snapshot sharing all unchanged refs; **remove a file** = a new snapshot whose tree omits
  it. Old snapshots stay valid and restorable.

A repository tracks **its own folder**: `Snapshot` walks `<folder>`, skipping the
`.hidage/` metadata directory, and `buildRoot` wraps it under a single top-level entry
named after the folder so it restores under its original name. `Snapshot` reports the new
objects it wrote (the delta) versus the total.

`snapshot --replace` supersedes the previous latest snapshot: the new snapshot chains to
the *replaced* snapshot's parent (so history stays valid), then the previous one is
forgotten and a prune reclaims the objects only it held. This serves the "keep just the
current state" workflow without losing the rest of the chain.

Restore streams each file chunk-by-chunk to disk (memory stays bounded regardless of file
size) and rejects path-traversal entry names.

### Diff (`Loader.Diff`)
Comparing two snapshots walks their trees, **skipping any subtree whose `Ref` is equal**,
so cost is proportional to the change, not the tree size. It reports each path as
added / removed / modified.

### Prune / GC (`Repo.Forget`, `Repo.Prune`)
`forget` deletes a snapshot's root record. `prune` then mark-sweeps **both stores**: it
computes the set of `CipherID`s reachable from *every surviving snapshot in this repository*
(root → trees → files → chunks) and deletes any object — meta node or data ref — not in
that set. Objects shared with a surviving snapshot are retained, so pruning one version
never harms another.

Crucially, prune is also safe across *different folders* sharing the data store: deleting a
data object only unlinks **this** repository's `refs/<repoID>/` link. The `HardlinkStore`
reclaims the pooled blob only when that was the last reference (link count drops to the
pool's own), so another folder's repository that still links the same chunk keeps it. No
cross-repository coordination or registry is needed — the filesystem refcount does it.

---

## Friend list (`internal/friends`)

A node keeps the list of its friends in `~/.config/hidage/friends.json` (no secrets):
per friend, the Ed25519 public key, a local nickname, the last known addresses, the quota
this node gives them, and when it last saw them. hidage is friends-only, so this list is
the whole trust model: `serve` refuses inbound connections from any other key (a libp2p
connection gater checked once the remote is authenticated) and accepts pushes only from
friends, and `publish` pushes to friends.

Friends are added from a **contact card**, `hidage1:` + base32 of a compact binary payload
(public key, suggested nickname, multiaddrs) with a 4-byte SHA-256 checksum so a damaged
copy-paste is caught. `hidage id` prints the node's card (from the running node's actual
addresses, published in `node.json`, or else from `listen` in `config.toml`); `hidage friend
add <card>` adds the friend or refreshes their addresses. **Invites** make pairing one step: `friend invite` stores a random 16-byte token (only its
hash is kept, with an expiry, 24 h by default) and prints this node's card with the token
appended. `friend add <invite>` adds the inviter and presents the token over
`/hidage/pair/1.0.0` along with its own card; the inviter's node checks that the card is the
requester's own key, consumes the token and adds the invitee, so the friendship is mutual.
If the inviter's node can't be reached yet, the invitee's running node retries until the
invite expires. While an invite is open the inviter's node lets unknown peers connect, but
serves them only pairing: head records, friends' address records and hosting all check
friendship. A legacy `trust.json` is imported
once (trusted keys become friends without addresses; blocked keys are dropped). The list
is re-read when its file changes, so `friend add/rm` applies to a running node.

---

## Networking (`internal/p2p`, `internal/netstore`)

**Reaching friends across NATs.** Nodes listen on TCP and QUIC (IPv4 and IPv6) and try, in
order: a direct connection (UPnP/NAT-PMP port mapping helps home routers), then a
connection through a **friend's circuit relay**, which DCUtR hole punching then tries to
upgrade to a direct one. `serve` offers the relay service to friends only (an ACL over both
ends of the circuit) and with no time or data limit, so a relayed link can carry backup
data when hole punching fails. A node that decides it is behind a NAT reserves slots on
friends' relays and advertises `/p2p/<relay>/p2p-circuit` addresses, which end up in its
contact card. `reachability` in `config.toml` controls the decision: `auto` (the default)
treats a node with no public IP on any interface as private, rather than waiting for
AutoNAT, which needs several peers to agree and so can stay undecided in a small group;
`public` (port forwarded) and `private` override it. libp2p only advertises circuit
addresses through relays with a public IP, so at least one friend must be reachable; a
small VPS running `serve` works as the group's relay, and overlay networks (Tailscale,
WireGuard, ZeroTier) work as plain addresses.

A node is a **libp2p host whose peer ID is its Ed25519 identity** (so a recovered node has
the same peer ID as the original). There is no global discovery (no DHT): a node talks
only to the friends in its list, and they are few enough to ask directly. It speaks:

- **Object protocol** (`/hidage/object/1.0.0`) — request an object by content address,
  receive its bytes. Objects are already encrypted/signed. A restoring node asks its
  reachable friends in turn.
- **Push protocol** (`/hidage/push/1.0.0`) — replicate a snapshot's objects to a peer that
  agrees to store them. The receiver identifies the sender from the secured connection's
  public key and stores only for **friends**.
- **Head protocols** (`/hidage/head/1.0.0`, `/hidage/head-get/1.0.0`) — a **signed head
  pointer** names the owner's **account-manifest** content address, so the owner can find
  *all* of its backups with no local state. The owner pushes it to its friends on every
  publish (a friend stores it only if the sender is the key that signed it); a recovering
  owner asks its friends for it back (they answer only the owner's key) and keeps the newest
  valid one.
- **Address protocol** (`/hidage/addr/1.0.0`) — each node signs a record of its current
  addresses (with a timestamp sequence number). A friend can ask a node for its own record
  or for the last record it holds of another of its friends, so a friend whose address
  changed can still be found through the others. Records are verified against the
  subject's key and only a newer one replaces an older one. `serve` reconnects to friends
  every few minutes and refreshes their records.

An owner can back up many folders under one identity, so the head doesn't name a single
backup — it names an **account manifest** (`internal/account`): a small object listing one
entry per repository (`repoID`, name, current Release id). It needs no signature of its
own — the signed head endorses its content address, and the fetcher verifies that address.

`publish` gathers a snapshot's object closure (`repo.PublishSet`: snapshot bytes + every
reachable DAG node + chunk), **erasure-codes** it (below), and spreads the resulting shards
round-robin across every reachable friend (dialed at stored addresses, then at addresses
learned from other friends), or the explicit `--peer` multiaddrs instead. Since the coding only helps across
enough *distinct* peers, it reports the fault tolerance and `--min-peers` guards thin
configurations. It then fetches the current account manifest,
upserts this repo's
entry (replacing it by `repoID`, never clobbering siblings), pushes the new Release + the
new manifest to every peer, and hands every friend reached a new signed head naming the
manifest. `net-restore` resolves the
head → fetches + verifies the manifest → selects a backup (`--repo <name>` / `--all` / the
sole entry) → fetches that Release, gathers K-of-M shards per pack from whoever still has
them, rebuilds the objects, and runs the ordinary restore walk against a local cache.
(`net-list` just prints the manifest.) On a machine with no friend list yet, `--from
<contact card | multiaddr>` names a friend to recover from.

---

## Durability: hosting, audits, erasure coding

These keep distributed data recoverable and make problems visible.

- **Hosted sets** (`internal/hosting`, push protocol `/hidage/push/2.0.0`) — a node keeps
  each friend's data in that friend's own directory. An owner publishes a repository by
  replacing its **set** on each friend (the set id is an HMAC of the repo id under the
  master key, so hosts can't link it to the repo): it lists the set's objects with their
  sizes, the host answers which ones it lacks and whether the result fits, only those are
  sent, and the host commits the new list. Renewing an unchanged backup therefore sends
  only the list. The **quota** is the space the host gives that friend (`friend set
  --give`), checked against the bytes the friend would actually hold afterwards, so
  repeated renewals never inflate it. Objects no set lists any more move to a per-friend
  **trash** and are deleted after `hosting.trash_days`, so a compromised owner machine that
  publishes garbage can't wipe its remote copies at once. A set not renewed within
  `hosting.lease_days` is dropped (its owner has gone away). Object and audit requests are
  answered from the requester's own data, live or in trash.

- **Proof-of-storage audits** (`/hidage/audit/1.0.0`, `internal/p2p`) — a node that holds
  the original challenges a peer with a shard id + fresh random nonce; the peer must return
  `SHA-256(nonce || shardBytes)` (only computable with the bytes), signed by its identity,
  within a short response **deadline** (a soft defense against fetching the shard from
  elsewhere on demand). The challenger verifies the proof and signature. `publish` records
  which shards went to which peer (`.hidage/distribution.json`); `audit` regenerates the
  published release deterministically and samples each friend's assigned shards. The
  outcome is kept per friend in `friends.json` and shown by `status` and `friend ls`.

- **Erasure-coded redundancy** (`internal/shard`, `internal/pack`) — objects are grouped
  into ≤8 MiB **packs**; each pack is Reed-Solomon coded (`klauspost/reedsolomon`) into M
  shards of which **any K reconstruct** it. Shards are spread round-robin across peers, so
  losing a minority of peers loses < M−K shards per pack and the data still recovers — at
  a fraction of the cost of M full replicas. *(Reed-Solomon replaced an earlier fountain-
  code library that silently returned corrupt output at some parameters.)*

---

## Threat model & guarantees

A summary here; the full adversarial analysis — free-rider/Sybil resistance, audit limits,
eclipse, whitewashing, and the friends-only trust model — is in
[`THREAT_MODEL.md`](THREAT_MODEL.md).

- **Confidentiality:** all stored objects are encrypted client-side; a host holding them
  sees only ciphertext. Metadata (file names, sizes, tree structure) lives inside
  encrypted DAG nodes.
- **Integrity:** content addressing (SHA-256) detects object corruption; AEAD tags detect
  tampering; the Merkle root binds the whole tree; snapshots are Ed25519-signed by the
  owner.
- **Anonymity:** *pseudonymous only* — peers are identified by public key; their IP
  addresses are visible to peers they connect to (network-level anonymity is out of scope).
- **Not yet covered:** at-rest forward secrecy (a leaked master key decrypts all snapshots
  in that repository); multi-writer concurrency (snapshots assume a single owner).

---

## Beyond the storage core — status & scope

Everything originally deferred past the local storage core is now built, except where
explicitly marked out of scope:

- **Networking** — ✅ *implemented* (`internal/p2p`, `internal/netstore`, see above):
  libp2p host, object/push/head/address protocols between friends, NAT traversal.
- **Friend list** — ✅ *implemented* (`internal/friends`): contact cards, friends-only
  connections and pushes.
- **Disaster recovery** — ✅ *implemented* (see below): recover from the network with only
  the recovery phrase + one friend's contact card.
- **Hosting, audits** — ✅ *implemented* (`internal/hosting`, audit protocol; see
  "Durability" above). The earlier reputation scores and contribution-ratio ledger were
  removed: between friends, a stated quota and the last audit result say more.
- **Erasure-coded redundancy** — ✅ *implemented* (`internal/shard` Reed-Solomon +
  `internal/pack`; see "Durability" above).
- **Storage from strangers / storage market** — *removed / out of scope.* hidage backs up
  to friends only (see [`docs/friends-only.md`](docs/friends-only.md)); there is no
  discovery of untrusted storage and no pricing/payment/contract layer.
- **Network-level anonymity** (hiding IPs via onion/mixnet routing) — *out of scope.*

### Disaster recovery — rebuild from the network with no local state

Surviving total loss of the original machine works today. To restore, a node walks the DAG
from the top, pulling each object from the network by its content address and decrypting it
with the master key (`fp` for each object is carried in its parent):

```
head pointer ──▶ snapshot ──▶ root Ref ──▶ tree nodes ──▶ file nodes ──▶ chunk Refs
 (from friends,    pull+verify    decrypt      decrypt        decrypt      pull + decrypt
  owner-signed)    (signature)
```

The three things this needs — all now in place:

1. **Distribute snapshots and DAG nodes, not just chunks.** ✅ `repo.PublishSet` gathers a
   snapshot's whole closure — the signed snapshot bytes, every reachable DAG node, and
   every chunk — and `publish` replicates all of it, so the recipe travels with the data
   rather than dying with the original `.hidage`.

2. **Make the keys recoverable.** ✅ Both the master key and the identity derive from a
   single 32-byte root, exportable as a 24-word BIP39 phrase (`recovery-code`) and
   restorable with `recover`.

3. **Find all backups with zero local state.** ✅ A **signed head pointer**
   (`account-manifest id`, signed by the identity key), handed to every friend on
   `publish` and returned only to the owner's key. Recovery needs only the identity (from the recovery
   code) to locate the head, and the manifest enumerates every repository — no snapshot or
   repo id to remember.

**Minimal recovery seed.** Recovering an arbitrarily large dataset needs only a tiny
off-machine secret plus a way onto the network: the **24-word recovery phrase** (which
reproduces both the master key and the identity) and one **friend's contact card**
(`--from`). From there `net-restore` does: identity → ask friends for the head → fetch +
verify the manifest and release → gather K-of-M shards from friends → reassemble → walk
the DAG, decrypting nodes and chunks.

See the [README roadmap](README.md#roadmap) for phase ordering.
