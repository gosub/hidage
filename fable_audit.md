# hidage — architecture & product audit

**Reviewer:** independent first-principles review (Fable 5)
**Date:** 2026-07-12
**Scope:** whole repository (~7.4k LOC Go), docs, and threat model. Build clean,
`go vet` quiet, full test suite green.

## Verdict

The cryptography and the local storage core are genuinely strong: a single-root
→ HKDF key hierarchy, keyed-convergent encryption that gets cross-folder dedup
without a confirmation oracle, a hardlink link-count as a registry-free GC
refcount, and a threat model that tells the truth. Docs, code, and tests agree —
rare.

Every serious weakness was in the **network/operations layer**, not the crypto:
durability invariants (leases, audits, DHT records) that decay in wall-clock time
with nothing maintaining them, a network path that re-uploads everything in
RAM-resident batches, and one outright remote crash. The findings below were
found in this review; the ones marked **Fixed** were implemented on the
`review-fixes` branch this session.

---

## New findings this review missed by the prior pass

Two were backed by direct experiment, one by grep:

1. **Remote nil-pointer crash (confirmed by reproduction).** A node built with
   nil blobs — every `publish`/`audit`/`net-restore`, via `p2p.New(id, nil, "")`
   — registered the object handler unconditionally, and the handler dereferenced
   `n.blobs.Get` with no nil check. Any connected peer opening a `/hidage/object`
   stream crashed the process, aborting an in-flight publish. **Fixed** in
   `b59d228`.

2. **Provider records expire and were never refreshed.** `serve` announced each
   hosted object to the DHT exactly once at startup; libp2p provider records have
   ~48h validity, so after a day `net-restore`'s provider discovery could no
   longer locate shards peers still held — silently time-windowing recovery to
   about a day. This is the same failure as the head-record expiry below, one
   layer down (the shards themselves). **Fixed** in `ad1344f`.

3. **Dead crypto code + stale package contract.** `crypto.Encrypt`/`Decrypt`
   (streaming AEAD) had zero non-test callers, and the `crypto` package doc still
   described the abandoned random-per-backup-DEK model rather than the
   deterministic sealing actually in use; `main.go` called the tool a
   "fountain-shards" MVP. **Fixed** in `eb5f8e5`.

---

## Findings and status

| # | Finding | Impact | Status |
|---|---------|--------|--------|
| 1 | Remote nil-blobs object-handler crash | High (correctness) | **Fixed** `b59d228` |
| 2 | Provider records expire, never reprovided | High (recovery) | **Fixed** `ad1344f` |
| 3 | DHT head record dies with the owner | High (recovery) | **Fixed** `46cff0f` |
| 4 | Push budget checked once/stream; unbounded object alloc | Med–High (economics/DoS) | **Fixed** `d7ab5f0` |
| 5 | Symlinks dropped silently; mtimes lost | High trust value | **Fixed** `c9e4682` |
| 6 | Dead crypto code + stale docs | Med (audit clarity) | **Fixed** `eb5f8e5` |
| 7 | Argon2id unlock on read-only commands | Med (ergonomics) | **Fixed** `a68ab2d` |
| 8 | No "am I safe right now?" — missing `status` | High | **Fixed** `1b5d447` |
| 9 | No owner-side maintenance agent | High | **Deferred** (partial) |
| 10 | Publish is O(dataset) in RAM/bandwidth; unstable placement | High | **Deferred** |
| 11 | Recovery is a dead end (`net-restore` rebuilds no repo) | Med | **Deferred** |
| 12 | `audit` manual/narrow; receipts dropped | Med | **Deferred** |
| 13 | Every snapshot re-reads/re-chunks the whole tree | Med | **Deferred** |
| 14 | Non-atomic writes in KeyedStore + JSON state files | Low (robustness) | **Deferred** |
| 15 | Account-manifest name collisions; `--all` overwrite | Low | **Deferred** |

### Fixed — detail

- **1 · nil-blobs crash.** Guard the object handler (audit handler already had
  the check); regression test that a nil-blobs node answers object requests
  cleanly.
- **2 · reprovide.** `reprovideLoop` re-announces hosted objects on a 12h
  interval (inside the ~48h validity), mirroring the storage-offer advertise
  loop.
- **3 · head survival.** New `/hidage/head/1.0.0` push protocol: on publish the
  owner hands each storage peer the signed head record; `serve` persists them
  (keyed by owner pubkey) and re-puts them to the DHT on a 12h loop. Records are
  owner-signed and re-validated on store and put, so a peer can neither forge nor
  alter what it re-serves. `PutHead` now returns the exact bytes it put.
- **4 · push budget + caps.** The accept callback now takes the cumulative bytes
  the stream would have stored and is consulted before each object, so the
  contribution ratio binds within a push. Objects capped at 16 MiB (above the
  4 MiB max chunk and ~256 KiB shards); id length bounded. Tests cover the
  cumulative cutoff and oversized rejection.
- **5 · fidelity.** Snapshot collects and reports unsupported entries
  (symlinks/devices) instead of dropping them silently; `ModTime` added to
  `FileNode`/`Entry` (omitempty, so old nodes decode) and re-applied on restore,
  directories last. Tests for mtime round-trip and symlink reporting.
- **6 · dead crypto.** Removed the streaming AEAD + `chunkNonce`/
  `DefaultChunkSize` and its tests; rewrote the `crypto` and `main` package docs.
  `NewDEK`/`WrapDEK`/`UnwrapDEK`/`DeriveKEK` stay (all used).
- **7 · selective unlock.** `snapshots` and `forget` (metadata-only, self-
  verifying) open via a new `discoverRepoNoKey` — no passphrase prompt, no
  Argon2id. Verified end-to-end.
- **8 · `hidage status`.** Reports snapshot freshness, published-vs-latest (via a
  new `snapshotID` in `distribution.json`), per-peer audit verdicts, fault
  tolerance, and failing peers; non-zero exit when degraded; runs without
  unlocking. Fault-tolerance/failing-peer assessment is a unit-tested pure
  function.

---

## Deferred — recommended next, in priority order

These are real and unaddressed; each is a focused effort of its own rather than
something to bundle into the fixes above.

1. **Owner-side maintenance loop (finding 9).** `serve` still only does GC +
   advertise + the two new reprovide/head-republish loops. The owner-side
   duties — re-publish/renew leases for every repo in the account manifest, run
   sampled audits on a schedule, warn when redundancy drops below tolerance —
   have no agent. This is the largest remaining product gap; `hidage status`
   (fixed) is the visibility half, this is the action half. An interim
   `hidage maintain` one-shot for cron would close most of it. **Med.**

2. **Incremental network publish (finding 10).** `repo.PublishSet` materializes
   the whole closure as a map and `pack.Build` roughly doubles it (>100 GB RAM
   for a 50 GB folder); the push protocol has no "already have it" negotiation,
   so a republish re-uploads everything; and `groupBySize` bins over globally
   sorted ids, so one new chunk shifts every later pack boundary and changes most
   shard ids — which defeats the deterministic-id lease-renewal story after any
   edit. Fixes: stream packs one ≤8 MiB pack at a time; add a `Has`-negotiation
   round; content-defined (not global-sort) pack boundaries + deterministic
   (sorted) peer assignment. Then a republish of an unchanged repo is a near-no-op
   that just renews leases. **High effort.**

3. **Recovery continuity — `net-restore --adopt` (finding 11).** Recovery writes
   files and stops: no `.hidage/` is rebuilt, chunks land in a throwaway cache,
   `distribution.json` is gone. The recovered machine cold-starts and re-uploads
   100% on its first publish. Rebuild `.hidage/` (original repoID, snapshots,
   meta objects) and seed the data store from the fetched chunks so the next
   publish renews existing shard ids. **Med.**

4. **`audit` usability (finding 12).** Requires `--peer <multiaddr>` though
   `distribution.json` already records peer pubkeys and `DialPeerID` can locate
   them; audits only the latest release; records one all-or-nothing outcome; and
   the signed receipts (the whole point) are computed then dropped. Make no-flag
   `audit` sweep every peer in every repo's distribution, persist receipts, and
   record per-shard outcomes. **Low**, and foldable into finding 9.

5. **Stat-cache snapshots (finding 13).** Snapshot re-reads and re-chunks the
   whole tree every run; use the parent snapshot as a name+size+mtime cache to
   reuse unchanged `FileNode` refs (mtime is now recorded — finding 5 unblocks
   this). Replace the double `objectIDs()` full-store scan with builder counters.
   **Med.**

6. **Atomic writes (finding 14).** `KeyedStore.Put` and the ledger/reputation/
   trust JSON files use `os.WriteFile` directly; a crash mid-write truncates
   them. A temp-file + rename helper (already present in `HardlinkStore`) for all
   four. **Low.**

7. **Manifest name collisions (finding 15).** `Entry.Name` is the folder
   basename, so two `notes` folders collide: `net-restore --repo notes` is
   ambiguous and `--all` interleaves them under one output dir. Detect at publish
   (warn), disambiguate output dirs by repoID suffix on collision. **Low.**

## Assumptions worth challenging (unchanged, still open)

- **`--peer` silently overridden by reputation** (`acceptablePeer`): explicit
  naming is the strongest user intent; it should bypass reputation or require
  `--force`, not skip with a one-line print.
- **Manifest lost-update race:** `publish` does fetch→upsert→put with no CAS; two
  devices under one identity (the recovery phrase invites reuse) publishing
  different repos concurrently clobber each other's entry. The manifest is
  repoID-keyed and already upserts — design the merge before multi-device lands.
- **Sequential network fetches:** `netstore.Get`/`Reassemble` fetch
  object-by-object; restore latency is O(objects × RTT). Bounded-concurrency
  parallel fetch is contained and high-payoff.
- **Reputation store has no mutex:** fine while `serve` only reads it, but the
  moment the maintenance loop (finding 9) records audits inside `serve`, it
  races.
- **Positioning via defaults:** the docs say friend-to-friend is the real mode;
  make `--trusted` the default publish source, `--open` the explicit opt-in.
