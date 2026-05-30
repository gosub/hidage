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

$HIDAGE_STORE (default /opt/hidage/store)
  pool/<id>            # one canonical encrypted copy of each chunk (the bulk)
  refs/<repoID>/<id>   # this repo's hard links into the pool — its reference set
```

`Init` writes `.hidage/` and its marker config; `Open` reads it. Both take the user
master key (loaded once via `internal/keyring`) and the data-store path. `Find` locates a
repository by walking up from the current directory, so commands work git-style from
anywhere inside a tracked folder; `snapshot` auto-creates `.hidage/` on first use. The
data store path comes from `$HIDAGE_STORE` or the `/opt/hidage/store` default; a
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

## Trust policy (`internal/trust`)

A node keeps a local policy of peer public keys it has an opinion about, in
`~/.config/hidage/trust.json` (no secrets — just public keys). When the networking
layer decides whether to place shards on a peer or accept them from one, it consults the
policy first:

```
Blocked  → never interact (overrides everything)
Trusted  → interact regardless of reputation (friend-to-friend, à la Syncthing pairing)
Neutral  → defer to the reputation score (Phase 4)
```

`Verdict(pubkey)` returns one of those three; trusted and blocked are mutually exclusive
(trusting a key clears any block and vice-versa). Managed via `hidage trust
add/remove/block/unblock/list`. The decision is a pure function of `(pubkey, policy)` plus
the future reputation score, so it is fully testable before any networking exists and
simply becomes load-bearing once peers are real. The networking layer consults it when
accepting pushed objects.

---

## Networking (`internal/p2p`, `internal/netstore`)

A node is a **libp2p host whose peer ID is its Ed25519 identity** (so a recovered node has
the same peer ID as the original). It speaks three things:

- **Object protocol** (`/hidage/object/1.0.0`) — request an object by content address,
  receive its bytes. Objects are already encrypted/signed, so serving to anyone is safe.
- **Push protocol** (`/hidage/push/1.0.0`) — replicate a snapshot's objects to a peer that
  agrees to store them. The receiver identifies the sender from the secured connection's
  public key and consults its **trust policy** (store unless `Blocked`), then re-announces
  what it stored to the DHT.
- **Kademlia DHT** (`/hidage` prefix) — `Provide`/`FindProviders` map an object's content
  address (a raw CIDv1 over its SHA-256) to the peers holding it, and **signed head
  pointers** (`/hidage/<ownerpubkey>` → latest snapshot id) let a peer find an owner's
  current snapshot with no local state. A namespaced record validator verifies the head
  signature on put and get and selects the newest.

`publish` gathers a snapshot's object closure (`repo.PublishSet`: snapshot bytes + every
reachable DAG node + chunk), **erasure-codes** it (below), spreads the resulting shards
across trusted peers, and posts a head pointer to the **Release** descriptor. `net-restore`
resolves the head, fetches the Release, gathers K-of-M shards per pack from whoever still
has them, rebuilds the objects, and runs the ordinary restore walk against a local cache.

---

## Durability: audits, reputation, accounting, erasure coding

These keep distributed data recoverable and keep peers honest.

- **Proof-of-storage audits** (`/hidage/audit/1.0.0`, `internal/p2p`) — a node that holds
  the original challenges a peer with an object id + fresh random nonce; the peer must
  return `SHA-256(nonce || objectBytes)` (only computable with the bytes), signed by its
  identity. The challenger verifies the proof against its own copy and the signature,
  producing a verifiable pass/fail **receipt**.

- **Reputation** (`internal/reputation`) — audit outcomes accumulate per peer into a
  Laplace-smoothed pass rate. The trust policy's *neutral* verdict defers here: a neutral
  peer is used once it has enough history and a high enough score, and avoided if its
  history is low-scoring. `publish` filters its target peers through `trust` then
  reputation.

- **Space accounting** (`internal/accounting`) — a per-peer ledger of bytes stored *for
  them* vs. *by them*. On accepting a push, a node enforces a contribution ratio: it
  stores for a peer at most `ratio × what they store for us + a free quota` (defaults 3×,
  1 GiB). This is the mechanism behind "contribute 2–4× to consume 1×".

- **Erasure-coded redundancy** (`internal/shard`, `internal/pack`) — objects are grouped
  into ≤8 MiB **packs**; each pack is Reed-Solomon coded (`klauspost/reedsolomon`) into M
  shards of which **any K reconstruct** it. Shards are spread round-robin across peers, so
  losing a minority of peers loses < M−K shards per pack and the data still recovers — at
  a fraction of the cost of M full replicas. *(Reed-Solomon replaced an earlier fountain-
  code library that silently returned corrupt output at some parameters.)*

---

## Threat model & guarantees

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
  libp2p host, object/push protocols, Kademlia DHT, signed head pointers.
- **Trust / whitelist** — ✅ *implemented* (`internal/trust`): prefer or block specific keys
  before reputation; consulted when accepting pushes.
- **Disaster recovery** — ✅ *implemented* (see below): recover from the network with only
  the recovery phrase + a bootstrap peer.
- **Reputation, audits, accounting** — ✅ *implemented* (`internal/reputation`,
  `internal/accounting`, audit protocol; see "Durability" above).
- **Erasure-coded redundancy** — ✅ *implemented* (`internal/shard` Reed-Solomon +
  `internal/pack`; see "Durability" above).
- **Automatic peer selection / storage market** — *not built.* You name storage peers
  explicitly (filtered by trust + reputation); discovering who offers storage is a
  marketplace concern, deliberately out of scope.
- **Network-level anonymity** (hiding IPs via onion/mixnet routing) — *out of scope.*

### Disaster recovery — rebuild from the network with no local state

Surviving total loss of the original machine works today. To restore, a node walks the DAG
from the top, pulling each object from the network by its content address and decrypting it
with the master key (`fp` for each object is carried in its parent):

```
head pointer ──▶ snapshot ──▶ root Ref ──▶ tree nodes ──▶ file nodes ──▶ chunk Refs
 (DHT, by         pull+verify    decrypt      decrypt        decrypt      pull + decrypt
  owner pubkey)   (signature)
```

The three things this needs — all now in place:

1. **Distribute snapshots and DAG nodes, not just chunks.** ✅ `repo.PublishSet` gathers a
   snapshot's whole closure — the signed snapshot bytes, every reachable DAG node, and
   every chunk — and `publish` replicates all of it, so the recipe travels with the data
   rather than dying with the original `.hidage`.

2. **Make the keys recoverable.** ✅ Both the master key and the identity derive from a
   single 32-byte root, exportable as a 24-word BIP39 phrase (`recovery-code`) and
   restorable with `recover`.

3. **Find the latest snapshot with zero local state.** ✅ An IPNS-style **signed head
   pointer** in the DHT, keyed by the owner's public key (`pubkey → latest snapshot id`,
   signed by the identity key), posted on `publish`. Recovery needs only the identity
   (from the recovery code) to locate the head — no snapshot id to remember.

**Minimal recovery seed.** Recovering an arbitrarily large dataset needs only a tiny
off-machine secret plus a way onto the network: the **24-word recovery phrase** (which
reproduces both the master key and the identity) and at least one **bootstrap peer**. From
there `net-restore` does: identity → DHT head lookup → fetch+verify snapshot → walk the
DAG, pulling and decrypting nodes and chunks from providers → reassemble.

See the [README roadmap](README.md#roadmap) for phase ordering.
