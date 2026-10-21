# hidage — Threat Model

This document states what hidage protects, what it assumes, and the **known
adversarial gaps** — honestly, so nothing is relied on that shouldn't be. See
[`DESIGN.md`](DESIGN.md) for how the mechanisms work.

## Roles

- **Owner** — backs up their own folders; publishes shards, audits, recovers.
- **Storage peer** — runs `daemon`, holds other people's encrypted shards.
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
  detect friends that drop data; `status` reports when that leaves the backup
  without enough fault tolerance.

## Trust model — friends only

hidage is backup **with friends**: you `friend add` the contact cards of people
you know, publish only to them, and `daemon` refuses inbound connections from
anyone else (a connection gater on the authenticated peer key). There is no
discovery of, or publishing to, strangers' storage. The incentive/Sybil problems
below are therefore largely moot: they apply only to a friend misbehaving, and
are listed for that case (see [`docs/friends-only.md`](docs/friends-only.md)).

## Known adversarial gaps

These are real and (mostly) unsolved. They affect availability/economics, **not**
confidentiality or integrity.

### Free-riding
Each friend gets the space you give it (`friend set --give`), whether or not it gives
you space back; `friend ls` shows both directions so imbalance is visible, and
fixing it is a conversation, not a protocol.

### Sybil attacks
Identities are costless, but with the friends-only model (no DHT, no free quota,
inbound connections only from friends) a minted identity has no way in: it is
useless until someone adds its contact card.

### Stolen identity cache
`identity.key` lets the daemon host for friends without the passphrase. It cannot decrypt
backups, but whoever reads it can act as you towards your friends: publish over your
backups (bounded by their trash) and fetch your (encrypted) data. Keep it like an SSH key;
deleting it only means a locked daemon can't start until you unlock once. On Windows it is
protected by the config folder's access list rather than a file mode.

### Saved passphrase
If you let `daemon --install` save the passphrase in the OS password store, anything
running as you with access to that store (a Windows Credential Manager entry, a Keychain
item, a Secret Service item) can read it and, with your keyring file, decrypt your
backups. That is the price of unattended backups; `passphrase_command` with a hardware
token, or a locked daemon, avoids it.

### Leaked invites
An invite lets whoever holds it pair once until it expires (24 h by default), like an
invite link: send it privately. `friend ls` shows every friend and `friend rm` revokes one.
While any invite is open, `daemon` accepts connections from unknown keys, but answers them
only on the pairing protocol.

### Audit outsourcing
The audit proves *someone* holds the bytes, not that *this* peer does. A lazy peer
could fetch a shard from a peer that actually stored it, then answer. The challenge
carries a 5-second response **deadline** (`internal/p2p`), so for non-trivial
objects the extra fetch should blow the budget — but it's a *soft* defense: tiny
objects can still be outsourced within the window. True proof-of-replication is not
implemented.

### Accept-then-delete
A friend can accept your shards then delete them. Audits catch this: a failed audit
is recorded against the friend and `status` flags it (and whether the backup is still
rebuildable); the response is social, plus re-publishing. The catch is that
detection is **only as frequent as audits run** (`audit_every` in the daemon, or
`hidage status --audit`) — between audits, your
redundancy can silently degrade. Mitigate by auditing on a schedule and publishing
at `--redundancy` high enough to tolerate some silent loss.

### Head-pointer rollback
Head records are owner-signed and newest-wins across all reachable friends, but a
friend can withhold its record or serve an older *valid* one, resolving a recoverer
to an older release (stale, not corrupt). It goes unnoticed only if every friend
reached is stale.

### Denial of service
Audits are cheap to send but force the holder to read+hash whole objects
(asymmetric, spammable). A friend can fill the quota you give it with junk. Only
friends can connect, so both are bounded by whom you add; neither is rate-limited yet.

### Owner machine compromised
Malware on an owner's machine (with the keyring unlocked) can publish garbage over its
backups. Hosts keep replaced data in trash for `hosting.trash_days` before deleting it,
and the owner can't empty a friend's trash remotely, so the old data survives long
enough to notice. Recovering from trash is not automated yet.

## Assumptions

- The user keeps the **24-word recovery phrase** safe and secret; anyone with it
  has full control, and losing it (and the keyring) makes data unrecoverable.
- At least one honest **friend** is reachable for recovery, and its contact card is
  kept with the recovery phrase.
- The local machine running the client is not compromised while the keyring is
  unlocked.
- Clocks are roughly synchronized (head-pointer newest-wins, storage leases).

## Out of scope (by design)

- **Network-level anonymity** — peers' IP addresses are visible to peers they
  connect to.
- **Backing up to strangers / a paid storage market** — there is no discovery of
  untrusted storage nodes and no pricing/payment/contract layer.
- **Costly identities / reputation systems** — unnecessary between friends.

## One-line summary

> Confidentiality and integrity are strong and cryptographic. Availability rests on
> the friends you publish to; audits tell you when one of them stops holding your data.
