# hidage — Design & Internals

This document is the technical reference for how hidage stores data. For a quick
conceptual overview and CLI usage, see the [README](README.md). Sections marked
*(planned)* are not implemented yet.

hidage has two storage models that share the same primitives (identity, crypto,
content-addressed store):

- **One-shot backup** (Phase 1) — encrypt + fountain-shard a file/folder into a static,
  signed manifest. Good for "freeze this and spread it redundantly."
- **Versioned repository** (Phase 2) — content-defined chunking + a Merkle DAG + signed
  snapshots, so updates re-store only the delta and history is kept. This is the model
  that supports change/update/remove.

---

## Shared primitives

### Identity (`internal/identity`)
A node has an Ed25519 keypair. The public key is its pseudonymous identity (and the
future libp2p peer key); the private key signs manifests and snapshots. The seed is
stored base64-encoded at `~/.config/hidage/identity.key` (mode 0600).

### Key hierarchy (`internal/crypto`)
- **KEK** — derived from the user's passphrase with **Argon2id** (defaults: time=3,
  memory=64 MiB, threads=4, 32-byte output, 16-byte random salt).
- **Data key** — a random 256-bit key (a per-backup DEK in Phase 1, a per-repository
  master key in Phase 2) is wrapped under the KEK with ChaCha20-Poly1305 (`nonce ||
  ciphertext`). Only the wrapped form is ever written to disk. A wrong passphrase fails
  the AEAD tag, which is how unlock errors are detected.

### Content-addressed store (`internal/store`)
A `Store` is a blob map keyed by the hex SHA-256 of its contents. The local `FSStore`
writes to `<root>/<id[:2]>/<id>` (mode 0600). `Put` is idempotent — storing identical
bytes twice is a no-op, which is the physical basis of deduplication. The interface
(`Put/Get/Has/Delete/List`) is the seam a libp2p-backed network store will implement in
Phase 3.

---

## Model 1 — One-shot backup (Phase 1)

```
file/folder ─pack─▶ blob ─encrypt─▶ ciphertext ─fountain─▶ shards ─▶ store
                                                                       │
   file/folder ◀─unpack─ blob ◀─decrypt─ ciphertext ◀─decode─ any K(+ε) of M
```

1. **Pack** (`internal/archive`) — a folder becomes a deterministic `tar` stream
   (entries name-sorted, timestamps zeroed); a single file passes through unchanged.
2. **Encrypt** (`internal/crypto`) — the blob is sealed with a random DEK using a
   **chunked AEAD**: ChaCha20-Poly1305 over 64 KiB plaintext chunks, where the per-chunk
   nonce is an 8-byte big-endian counter with its final byte set to `1` on the last
   chunk. The final-chunk flag gives **truncation protection** — a stream missing its
   tail presents no flagged chunk and fails to open.
3. **Shard** (`internal/shard`) — the ciphertext is expanded with the **R10 Raptor**
   fountain code (`google/gofountain`). It is split into `K = clamp(ceil(len / 64 KiB),
   4, 8192)` source symbols (alignment 4) and emitted as `M = ceil(K × redundancy)`
   encoded symbols. The code is systematic (IDs `0..K-1` reproduce the source symbols;
   `K..M-1` are repair). `BlockCode` passes through `uint16`, so `M < 65536`. Decoding
   needs any `K` symbols plus a small coding overhead — order-independent.
4. **Manifest** (`internal/manifest`) — a signed JSON recipe records everything needed
   to restore: crypto params + wrapped DEK, fountain params (`K`, `M`, alignment,
   ciphertext length), and the shard list (`{id, blockCode, size}`). It is Ed25519-signed
   over its canonical serialization (all fields except `signature`). **Keep this file** —
   it is the recovery recipe.

Restore reverses the pipeline and verifies the recovered plaintext against the
manifest's SHA-256.

**Trade-off:** the whole folder is one blob, so changing one byte re-encrypts and
re-shards everything. Model 2 exists to fix that.

---

## Model 2 — Versioned repository (Phase 2)

A **repository** (`internal/repo`) is a directory:

```
<repo>/
  hidage-repo.json     # {version, repoID, argon2 params, wrappedMasterKey}
  store/               # content-addressed encrypted objects (chunks + DAG nodes)
  snapshots/<id>.json  # one signed snapshot root per version
```

`repo init` generates a random per-repo **master key**, wraps it under the passphrase
KEK, and writes the config. `repo open` re-derives the KEK and unwraps the master key.

### Content-defined chunking (`internal/chunk`)
Files are split with **FastCDC** (`github.com/jotfs/fastcdc-go`, average 1 MiB, min 256
KiB, max 4 MiB, seed 0). Because boundaries are chosen from a rolling hash of the
*content*, inserting or removing bytes in the middle of a file only disturbs the chunks
around the edit — the rest realign and keep their identities. This is what makes
incremental snapshots cheap. (The same input always chunks identically.)

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

A directory's `TreeNode` references child trees and files by `Ref`; a snapshot
references a single root `TreeNode`. Because a `Ref` is determined by content, an
unchanged file or subtree has the **same `Ref` across snapshots** and is never
re-stored — exactly git's blob/tree model:

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
encryption confirmation attack). Keying the fingerprint with the per-repo secret means
dedup works **within the owner's own data** but leaks no chunk-equality across users.

### Snapshots & history (`internal/repo`)
A snapshot is a signed root:

```
Snapshot { version, id, createdAt, parent, source, root: Ref, ownerPubKey, signature }
```

- `signature` — Ed25519 over the canonical payload (all fields except `id` and
  `signature`).
- `id` — hex SHA-256 of that same payload (content-addressed; any prefix that is
  unambiguous resolves it on the CLI).
- `parent` — the previous snapshot's `id`, forming a git-like chain. **Update** = a new
  snapshot sharing all unchanged refs; **remove a file** = a new snapshot whose tree
  omits it. Old snapshots stay valid and restorable.

`buildRoot` wraps the source under a single top-level entry named after it, so both files
and directories restore under their original name.

Restore streams each file chunk-by-chunk to disk (memory stays bounded regardless of
file size) and rejects path-traversal entry names.

### Diff (`Loader.Diff`)
Comparing two snapshots walks their trees, **skipping any subtree whose `Ref` is equal**,
so cost is proportional to the change, not the tree size. It reports each path as
added / removed / modified.

### Prune / GC (`Repo.Forget`, `Repo.Prune`)
`forget` deletes a snapshot's root record. `prune` then mark-sweeps: it computes the set
of `CipherID`s reachable from *every surviving snapshot* (root → trees → files → chunks)
and deletes any stored object not in that set. Objects shared with a surviving snapshot
are retained, so pruning one version never harms another.

---

## Threat model & guarantees

- **Confidentiality:** all stored objects are encrypted client-side; a host holding
  shards/objects sees only ciphertext. Metadata (file names, sizes, tree structure)
  lives inside encrypted DAG nodes.
- **Integrity:** content addressing (SHA-256) detects corruption of any object; AEAD
  tags detect tampering; the Merkle root binds the whole tree; manifests and snapshots
  are Ed25519-signed by the owner.
- **Anonymity:** *pseudonymous only* — peers are identified by public key. IP-level
  anonymity is a later phase.
- **Not yet covered:** at-rest forward secrecy (a leaked master key decrypts all
  snapshots in that repo); multi-writer concurrency (snapshots assume a single owner).

---

## What's deliberately deferred

- **Fountain re-sharding of repository objects** — Model 2 stores encrypted chunks
  directly. Fountain coding buys redundancy across *unreliable peers*, so it is layered
  on in the networking phase (objects grouped into ~4–16 MiB packs, each fountain-sharded
  for distribution) rather than wasted on local storage. *(planned, Phase 3)*
- **Networking** — go-libp2p host, Kademlia DHT discovery, pack/shard transfer behind the
  `Store` interface. *(planned, Phase 3)*
- **Trust / whitelist** — a policy consulted before reputation, letting a node prefer or
  block specific keys (friend-to-friend). *(planned, Phase 3)*
- **Reputation** — proof-of-storage audits, signed receipts, space accounting for the
  2–4× contribution model. *(planned, Phase 4)*
- **Anonymity routing** — optional Tor / Veilid transport. *(planned, Phase 5)*

See the [README roadmap](README.md#roadmap) for phase ordering.
