# hidage — Threat Model

This document states what hidage protects, what it assumes, and the **known
adversarial gaps** — honestly, so nothing is relied on that shouldn't be. See
[`DESIGN.md`](DESIGN.md) for how the mechanisms work.

## Roles

- **Owner** — backs up their own folders; publishes shards, audits, recovers.
- **Storage peer** — runs `serve`, holds other people's encrypted shards.
- Every node is identified by an **Ed25519 public key**, which is also its libp2p
  peer ID. Identities are free to create.

## What is protected (and how)

- **Confidentiality.** Everything stored or transmitted is encrypted client-side
  (ChaCha20-Poly1305 under a key derived from your passphrase). A storage peer
  sees only opaque, content-addressed blobs — file names, sizes, and tree
  structure all live inside *encrypted* DAG nodes. A leaked shard reveals nothing
  without the master key.
- **Integrity.** Content addressing (SHA-256) detects any corrupted object; AEAD
  tags detect tampering; the Merkle root binds the whole tree; snapshots and head
  pointers are Ed25519-signed by the owner. Reassembly hash-verifies each shard,
  so a peer serving wrong bytes is ignored.
- **Authenticity / discovery.** The head pointer (`pubkey → account-manifest id`)
  is signed; the manifest is content-addressed and that address is verified, so an
  owner's backups can be found and trusted from their public key alone.
- **Durability.** Reed-Solomon erasure coding spreads M shards across peers so any
  K reconstruct — surviving the loss of a minority. Proof-of-storage **audits**
  detect peers that drop data; **reputation** steers future placement away from
  them.

## Trust model — two regimes

hidage has two modes, with very different guarantees:

1. **Friend-to-friend (strong).** You `trust add` the public keys of people you
   know and publish only to them. The incentive/Sybil problems below largely
   evaporate, because participation is gated by out-of-band social trust. **This
   is the mode to rely on for important data.**
2. **Open network (best-effort).** Publishing to/serving strangers. Confidentiality
   and integrity still hold, but the *economic* guarantees are weak — see below.

## Known adversarial gaps

These are real and (mostly) unsolved. They affect availability/economics, **not**
confidentiality or integrity.

### Free-riding via the free quota
`serve` extends every peer a `--free` quota (default 1 GiB) before requiring
reciprocation. A node that serves stingily (`--ratio 0 --free 0`) but publishes
widely can extract `free × (#hosts)` of storage for nothing. The free quota is a
deliberate cold-start credit line (you can't bootstrap reciprocity without it);
the cost is bounded free-riding. Tunable via `--free`, but there is no full fix in
the open network.

### Sybil attacks
Identities are costless, so an attacker can mint many and multiply the per-identity
free quota — the dominant Sybil lever. What is **not** vulnerable: reputation and
accounting are **local and self-written** (your view = your own audits; credit is
only written by your own `publish`), so Sybils cannot inflate their standing with
you or trick your `serve` into over-accepting beyond `free`. Residual risks:
**eclipse** (many Sybil IDs crowding a victim's DHT key to censor discovery —
redundancy limits data loss but not censorship) and **whitewashing** (a peer with
bad reputation discards its identity and rejoins clean with a fresh free quota).
The robust mitigation is the friend-to-friend regime; costly identities
(proof-of-work/stake) are out of scope.

### Audit outsourcing
The audit proves *someone* holds the bytes, not that *this* peer does. A lazy peer
could fetch a shard from a peer that actually stored it, then answer. The challenge
carries a 5-second response **deadline** (`internal/p2p`), so for non-trivial
objects the extra fetch should blow the budget — but it's a *soft* defense: tiny
objects can still be outsourced within the window. True proof-of-replication is not
implemented.

### Accept-then-delete
A peer can accept your shards (earning storage credit) then delete them. Audits
catch this: a failed audit drops the peer's reputation *and* revokes its accounting
credit with you, so it can no longer push to your `serve`. The catch is that
detection is **only as frequent as you run `hidage audit`** — between audits, your
redundancy can silently degrade. Mitigate by auditing on a schedule and publishing
at `--redundancy` high enough to tolerate some silent loss.

### Reputation has no decay
Scores are a cumulative (Laplace-smoothed) pass rate, so a peer can bank many
passes and then misbehave while staying "good" for a while. An EWMA / recency
weighting would fix this; not yet implemented.

### Head-pointer rollback
Head records are owner-signed and newest-wins, but an eclipsing peer could serve an
older *valid* signed head, resolving a recoverer to an older release (stale, not
corrupt). Bounded by the same eclipse difficulty.

### Denial of service
Audits are cheap to send but force the holder to read+hash whole objects
(asymmetric, spammable). Pushed junk under the free quota fills disks until the
lease GC reclaims it. Neither is rate-limited yet.

## Assumptions

- The user keeps the **24-word recovery phrase** safe and secret; anyone with it
  has full control, and losing it (and the keyring) makes data unrecoverable.
- At least one honest **bootstrap peer** is reachable for discovery/recovery.
- The local machine running the client is not compromised while the keyring is
  unlocked.
- Clocks are roughly synchronized (head-pointer newest-wins, storage leases).

## Out of scope (by design)

- **Network-level anonymity** — peers' IP addresses are visible to peers they
  connect to.
- **A paid storage market / automatic peer discovery** — you name storage peers
  explicitly.
- **Costly identities / global consensus reputation** — kept local and
  Sybil-isolated instead.

## One-line summary

> Confidentiality and integrity are strong and cryptographic. Availability and the
> storage economy are **best-effort on the open network** and **robust only in the
> friend-to-friend (trusted-peer) regime** — use trusted peers for anything you
> can't afford to lose.
