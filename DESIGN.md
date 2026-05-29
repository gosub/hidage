# hidage — Design & Internals

This document is the technical reference for how hidage stores data. For a quick
conceptual overview and CLI usage, see the [README](README.md). Sections marked
*(planned)* are not implemented yet.

hidage stores data as a **versioned repository**: encrypted, content-addressed objects
plus a chain of signed snapshots. Content-defined chunking and a Merkle DAG mean an
update re-stores only what changed; history is kept and old versions stay restorable.

---

## Shared primitives

### Identity (`internal/identity`)
A node has an Ed25519 keypair. The public key is its pseudonymous identity (and the
future libp2p peer key); the private key signs snapshots. The seed is stored
base64-encoded at `~/.config/hidage/identity.key` (mode 0600).

### Key hierarchy (`internal/crypto`, `internal/userkey`)
- **KEK** — derived from the user's passphrase with **Argon2id** (defaults: time=3,
  memory=64 MiB, threads=4, 32-byte output, 16-byte random salt).
- **Master key** — a single random 256-bit key **per user** (not per repository),
  generated at `hidage init`, wrapped under the KEK with ChaCha20-Poly1305
  (`nonce || ciphertext`), and stored at `~/.config/hidage/masterkey.json`. Only the
  wrapped form is written. A wrong passphrase fails the AEAD tag, which is how unlock
  errors surface. Because every repository encrypts with this one key, identical chunks
  produce identical ciphertext **across all your folders**, so the shared data store
  deduplicates them globally.

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
master key (loaded once via `internal/userkey`) and the data-store path. `Find` locates a
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

## Threat model & guarantees

- **Confidentiality:** all stored objects are encrypted client-side; a host holding them
  sees only ciphertext. Metadata (file names, sizes, tree structure) lives inside
  encrypted DAG nodes.
- **Integrity:** content addressing (SHA-256) detects object corruption; AEAD tags detect
  tampering; the Merkle root binds the whole tree; snapshots are Ed25519-signed by the
  owner.
- **Anonymity:** *pseudonymous only* — peers are identified by public key. IP-level
  anonymity is a later phase.
- **Not yet covered:** at-rest forward secrecy (a leaked master key decrypts all snapshots
  in that repository); multi-writer concurrency (snapshots assume a single owner).

---

## What's deliberately deferred

- **Networking** — go-libp2p host, Kademlia DHT discovery, and object transfer behind the
  `Store` interface. *(planned, Phase 3)*
- **Fountain-coded distribution** — the rateless coding primitive already exists
  (`internal/shard`, R10 Raptor via `google/gofountain`: split data into `K` source
  symbols, emit `M ≥ K` encoded shards, reconstruct from any `K`-plus-overhead subset).
  It buys redundancy across *unreliable peers*, so it is layered on with networking —
  repository objects grouped into ~4–16 MiB packs, each fountain-sharded for distribution
  — rather than wasted on local storage. *(planned, Phase 3)*
- **Trust / whitelist** — a policy consulted before reputation, letting a node prefer or
  block specific keys (friend-to-friend). *(planned, Phase 3)*
- **Reputation** — proof-of-storage audits, signed receipts, space accounting for the 2–4×
  contribution model. *(planned, Phase 4)*
- **Anonymity routing** — optional Tor / Veilid transport. *(planned, Phase 5)*

See the [README roadmap](README.md#roadmap) for phase ordering.
