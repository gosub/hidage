# Design note: friends-only hidage

**Status:** implemented (steps 1–10, October 2026). Deviations from the text below:
`backup` also adds the folder to the daemon's schedule (`--once` to skip); `restore --friends
[--from card]` replaces `--from-friends [card]`; `status --audit` runs an audit on demand;
the head record keeps its timestamp instead of gaining a `seq`; packing also separates small
(metadata) objects from bulk chunks. Open questions 1, 3 and 4 were resolved as proposed
(no relay cap, identity cache, one identity per device); 2 (report imbalance only) is shown
by `friend ls`; 5 (Tor) is not done.
**Scope:** the network, trust and CLI layers. The local storage core (`chunk`,
`crypto`, `dag`, `repo`, `store`, `keyring`) and erasure coding (`pack`, `shard`) are
unchanged.

## 1. Decision

hidage becomes **backup with friends**: you store encrypted, erasure-coded shards on
machines of a small group of people you know (typically 2 to 10), and they store theirs
on yours. Backing up among internet strangers is dropped, not deferred.

Consequences:

- No Kademlia DHT. Everyone you talk to is in your friend list, so there is nothing to
  discover, only friends to reach.
- No storage economy. Reputation, contribution ratios and the open free quota exist to
  police strangers; between friends they are replaced by a stated agreement ("I give
  Alice 200 GB") and a health check.
- The hard problem moves to **reachability** (NAT traversal) and **pairing** (getting two
  machines to know each other), so that is where the new design effort goes.
- The CLI shrinks to one command per user story (section 7).

## 2. Goals and non-goals

Goals:

1. Pairing two friends takes one copy-paste each way at most.
2. Works between home connections behind NAT, without port forwarding, as long as at
   least one friend in the group is reachable (or a relay is configured).
3. Set and forget: a daemon keeps backups fresh, renewed and audited.
4. Disaster recovery needs only the recovery phrase plus one friend's contact.
5. About 9 top-level commands. Tuning lives in a config file, not in new commands or flags.

Non-goals:

- Discovering or using storage from strangers.
- IP anonymity between friends (friends see each other's IPs; Tor transport is a
  possible later option, section 10).
- Multi-device writers under one identity (unchanged from today; see open questions).

## 3. What is removed

| Removed | Where today | Why it is no longer needed |
|---|---|---|
| Kademlia DHT, provider records, `reprovideLoop` | `p2p/dht.go`, `p2p/object.go:65`, `cmd/hidage/net.go` | Shard locations are known (we pushed them) and the holder set is small enough to ask directly. |
| DHT head records, validator, `republishHeadsLoop` | `p2p/head.go` | Heads are already pushed to friends (`PushHead`); they are read back from friends. |
| `FindPeer` / `DialPeerID` | `p2p/dht.go:106` | Replaced by the address book (section 5.3). |
| Storage rendezvous, `advertiseLoop`, `--open`, `--open-peers` | `p2p/dht.go:29-34`, `net.go` | No strangers. |
| Transitive reputation, `/hidage/reputation/1.0.0` | `p2p/repquery.go`, `net.go` (`buildAnchors`, `aggregateTransitive`) | No strangers to rank. |
| Recency-weighted reputation scores | `internal/reputation` | Replaced by per-friend health (last audit result and time). |
| Ratio / free-quota economics | `internal/accounting`, `serve --ratio --free` | Replaced by an explicit per-friend quota. Also removes the double-counting bug where every lease renewal re-adds bytes to the ledger. |
| Block list, "unknown peer" handling | `internal/trust` | Default deny: only friends may connect to hidage protocols. |
| `--bootstrap` everywhere, the 15 s `WaitForPeers` stall | `net.go`, `netlist.go`, `reputation.go` | Nodes dial friends from the address book. |

Roughly 37 of the 673 packages in the current build (kad-dht, boxo, ipfs, ipld,
datastore) disappear with the DHT, plus their transitive dependencies.

Kept: the crypto and repository core, erasure coding, the push, object, audit and
head-push protocols (revised below), signed heads and the account manifest.

## 4. Concepts

- **Friend:** a peer identified by its Ed25519 public key, with a local nickname, last
  known addresses, the quota *I give them*, and health data. Friendship is mutual: both
  sides have each other in their list.
- **Contact card:** what you hand someone so their node can reach yours: public key,
  nickname suggestion, current addresses (including relay addresses). Printed by
  `hidage id`. It is not secret.
- **Invite:** a contact card plus a short-lived, single-use pairing token, so the
  receiving side can be added without the inviter typing anything back (section 5.1).
- **Hosted set:** what a host stores for one owner's repository: a directory of
  objects, replaced as a whole by each publish (section 5.4).

## 5. Design

### 5.1 Pairing

```
alice$ hidage friend invite            # prints: hidage1:<base32 blob>  (valid 24 h, one use)
bob$   hidage friend add hidage1:...   # Bob dials Alice, presents the token
                                       # both nodes now list each other as friends
```

1. `invite` creates a random 16-byte token, records it locally with an expiry, and
   prints `hidage1:` + base32(pubkey, addresses, token, suggested nickname).
2. `add` on Bob's side dials Alice over `/hidage/pair/1.0.0` and sends the token plus his
   own contact card. Alice's node checks and consumes the token and stores Bob as a
   friend, and replies with her card. Both store each other with the default quota from
   config (overridable later with `friend set`).
3. If Alice's node is not running or not reachable at that moment, Bob's node keeps the
   pending pairing and retries from the daemon until the token expires.
4. Fallback when no dial is possible at all (both behind hard NAT, no relay yet):
   `friend add <contact-card>` on both sides adds the other one-sidedly; the friendship
   becomes active once either side can connect.

The token is a capability: anyone holding the invite string can pair once within 24 h.
That is the same trade-off as Syncthing or Tailscale invite links and is acceptable for
something sent over a chat app. A spoken short code (magic-wormhole style) would need a
rendezvous server and is out of scope.

### 5.2 Connectivity

libp2p stays, minus the DHT, with its NAT features turned on:

| Option | Purpose |
|---|---|
| TCP + QUIC transports | QUIC improves hole-punching success. |
| `NATPortMap()` | UPnP / NAT-PMP port mapping on home routers that allow it. |
| AutoNAT (client and, on reachable nodes, service for friends) | Each node learns whether it is publicly reachable. |
| Circuit Relay v2 **service** on reachable nodes | A reachable friend relays for the group. ACL: reservations only from friends. Resource limits raised (no per-connection data/time cap for friends), because relayed links may carry backup data, not only hole-punch coordination. |
| `EnableAutoRelayWithStaticRelays(friends)` | Unreachable nodes reserve slots on reachable friends and advertise `/p2p/<relay>/p2p-circuit/p2p/<me>` addresses. |
| `EnableHolePunching()` (DCUtR) | Upgrade relayed connections to direct ones when the NATs allow it. |
| Connection gater | Refuse any peer not in the friend list (except during pairing, which is token-gated). |

Expected behaviour:

- At least one friend reachable: everyone can connect. Most pairs end up direct after hole
  punching, the rest stay relayed through that friend (using their bandwidth).
- Nobody reachable: configure a relay in `config.toml`. That can be any machine running
  `hidage daemon` with a public IP (a small VPS), with `relay = true`. No new command.
- Overlays always work: on Tailscale, ZeroTier, Nebula or plain WireGuard, the overlay
  IPs are just more addresses in the contact card. The docs will recommend this for
  difficult networks.

### 5.3 Address book

`~/.config/hidage/friends.json` replaces `trust.json`:

```json
{
  "<pubkey hex>": {
    "nickname": "bob",
    "addrs": ["/ip4/…/udp/4001/quic-v1", "/p2p/<relay>/p2p-circuit"],
    "addrsSeq": 42,
    "give": "200GiB",
    "lastSeen": "2026-10-20T15:00:00Z",
    "lastAudit": {"at": "…", "ok": true, "sampled": 4}
  }
}
```

Addresses change (DHCP, roaming laptops), so they are refreshed from friends:

- Each node signs an **address record** `{pubkey, addrs, seq}`. On every connection
  friends exchange their own record (`/hidage/addr/1.0.0`), newest `seq` wins.
- To find friend X, a node can ask any connected friend "do you have a record for X?".
  The answer comes only if X is also that friend's friend, so the protocol reveals nothing
  about a friend graph you don't already share.
- Relay addresses through a reachable friend are stable, which covers most roaming.

### 5.4 Storing for friends: hosted sets, quota, expiry

Host layout: `$STORE/hosted/<owner pubkey>/<set id>/<object id>`, where the set id is an
opaque value the owner derives from the repoID (HMAC under the master key), so hosts
don't learn repo identifiers.

**Push v2** (`/hidage/push/2.0.0`) replaces "push everything, renew by rewriting":

1. Owner sends the full list of object ids the set should contain.
2. Host replies with the ids it is missing.
3. Owner streams only the missing objects.
4. Host atomically switches the set to exactly that list: present ids are kept (and their
   lease renewed), ids no longer listed move to the set's **trash**.

Effects:

- Re-publishing an unchanged repo transfers only the id list.
- Bytes held per owner is the actual size of `hosted/<owner>/` (excluding trash), so the
  quota check is "held + incoming ≤ give". No cumulative ledger, nothing to drift.
- Removing a friend: their hosted data stays until it expires, or is deleted at once with
  `friend rm --purge`.

**Expiry (lease):** a set not touched by its owner for the lease period (default 90 days,
in config) is deleted. `status` on the owner side warns when any friend's copy is within
14 days of expiry; the daemon renews long before that.

**Trash:** objects dropped from a set stay in trash for `trash_days` (default 30) before
deletion, and do not count against the quota. This is the guard against a compromised or
ransomware-hit owner machine publishing a set that wipes its remote history: the owner
cannot purge a friend's trash remotely. Recovery from trash is a later feature; the point
here is that the data survives long enough to notice.

### 5.5 History on friends

Today only the latest release lives on the network. With hosted sets, the owner can keep
several releases in the same set: the id list is the union of the retained releases'
shards. Retention is configured, for example:

```toml
[retention]
keep_last = 3
keep_daily = 7
keep_weekly = 4
```

Shards that releases share are stored once, so this costs little more than the latest
release, as long as pack boundaries stay stable across releases (content-defined pack
boundaries, prior audit finding 10). Without that, every release changes most shard ids
and retention multiplies storage, so stable packing is a prerequisite for this section.

### 5.6 Heads and the account manifest

- `publish` signs a head record (unchanged format, now with a monotonic `seq` next to
  `createdAt`) naming the account manifest, and pushes head + manifest to every friend
  holding any set of ours.
- Reading the head means asking all reachable friends and taking the newest valid
  signature. A friend can withhold or serve an old head, but cannot forge one; the
  newest-wins rule across friends limits this (section 8).
- The manifest gets one new field: **`roster`**, the owner's friend list (keys,
  nicknames, last addresses, quotas), sealed under a key derived from the master key.
  Friends store it but cannot read it. This is what lets recovery find all friends from
  just one.

### 5.7 Disaster recovery

```
hidage init --recover          # phrase from stdin, new passphrase
hidage restore --from-friends <contact-card> --to ~/restored
```

1. Dial the friend from the contact card. Our public key is the same as before, so the
   friend recognises us as a friend and answers.
2. Fetch our head and manifest from that friend, open the sealed roster, write
   `friends.json`.
3. Dial every friend in the roster, take the newest valid head across all of them, then
   fetch the manifest and releases and gather K-of-M shards from whoever has them.
4. Restore files and **adopt** the repository: rebuild `.hidage/` with the original
   repoID and snapshots, and seed the local store from the fetched chunks, so the next
   `backup` only renews existing sets instead of uploading everything (prior audit
   finding 11).

**Recovery kit:** `init` and every `friend add` remind you to keep the kit up to date;
`hidage id --kit` prints the phrase plus your friends' contact cards, as text to print
or store offline.

### 5.8 Daemon

`hidage daemon` is the one long-running process:

- hosts friends' sets, serves objects to their owners, acts as relay if reachable;
- takes scheduled snapshots of configured folders, publishes them, prunes per retention;
- samples audits of every friend holding our sets (per-shard results kept as health,
  signed receipts persisted);
- refreshes address records and retries pending pairings.

It ships with a systemd user unit and a launchd plist.

**Keys:** hosting, relaying and auditing need only the identity key; snapshotting needs
the master key. Proposal: cache the identity key separately (`identity.key`, 0600,
derivable again from the root secret) so the daemon can host unattended after a reboot,
and obtain the master key via `passphrase_command` (e.g. `systemd-creds`, `pass`, a
keychain) or an interactive unlock at start. Without the master key the daemon keeps
hosting and reports "backups paused, locked" in `status`.

### 5.9 Who may fetch what

- Only friends can open hidage protocols at all (connection gater).
- Object requests are answered from `hosted/<requester pubkey>/` only: a friend can fetch
  back its own data, never another friend's. (Today any peer can fetch any object; the
  data is encrypted, but this removes the free-download surface.)
- Audits are answered for the challenger's own sets only.

## 6. Configuration

`~/.config/hidage/config.toml`, created by `init` with commented defaults:

```toml
store = "~/.local/share/hidage/store"   # default moves out of /opt
listen = ["/ip4/0.0.0.0/udp/4001/quic-v1", "/ip4/0.0.0.0/tcp/4001"]
relay = "auto"                # serve as relay for friends when publicly reachable
extra_relays = []             # contact cards of relays for groups with no reachable node
passphrase_command = ""       # e.g. "systemd-creds decrypt hidage.cred -"

[hosting]
default_give = "100GiB"
lease_days = 90
trash_days = 30

[backup]
redundancy = 2.0
min_friends = 2               # refuse to publish with less fault tolerance than this
folders = ["~/Documents", "~/projects"]
schedule = "hourly"
exclude_file = ".hidageignore"

[retention]
keep_last = 3
keep_daily = 7
keep_weekly = 4
```

## 7. Command surface

| Command | User story | Replaces |
|---|---|---|
| `hidage init [--recover]` | Set up this machine (keyring, store, config), or recover the keyring from the phrase. | `init`, `recover` |
| `hidage id [--kit]` | "What do I give my friend?" Contact card; `--kit` adds phrase + friends' cards. | `recovery-code`, the pubkey line of `init` |
| `hidage friend invite \| add \| rm \| set \| ls` | Manage the group. `ls` shows reachability, space given and used both ways, last audit. | `trust *` (5), `reputation`, `accounting` |
| `hidage backup [dir] [--local]` | Snapshot and publish to friends now. `--local` snapshots only. | `snapshot`, `publish` |
| `hidage restore [snap\|latest] [path] --to <dir> [--from-friends [card]]` | Get files back, one path or everything, locally or from friends. Refuses a non-empty target without `--force`. | `restore`, `net-restore` |
| `hidage log [--diff A B] [--friends]` | History, changes between versions, what friends hold. | `snapshots`, `diff`, `net-list` |
| `hidage forget <snap> \| --apply-retention` | Drop versions; prunes automatically. | `forget`, `prune` |
| `hidage status [-v]` | "Am I protected?" Freshness, fault tolerance, friend health, expiry, locked daemon. Non-zero exit when degraded. | `status`, `audit` (read side) |
| `hidage daemon` | Run in the background (section 5.8). | `serve`, scheduled `audit` |

That is 9 top-level commands, down from 18. Rules for adding anything later:

1. A new top-level command must correspond to a new user story, not a new mechanism.
2. Tuning goes in `config.toml`; one-off overrides are flags on the existing command.
3. Diagnostics go under `status -v` or `friend ls -v`.

## 8. Threat model changes

- **Gone:** Sybil attacks, free-riding via the free quota, transitive-reputation
  collusion, stranger denial of service through the push protocol. The connection gater
  closes hidage protocols to non-friends.
- **Unchanged:** confidentiality and integrity are cryptographic; a friend learns only
  ciphertext sizes and timing.
- **Still open, now between friends:**
  - *Accept-then-delete:* a friend drops our data. Detected by audits, shown in `status`;
    the response is social (talk to them) plus redundancy.
  - *Audit outsourcing:* a friend fetches shards from another friend to answer audits.
    Mitigated by `hosted/<owner>` scoping (friends can't fetch each other's data from
    you) and the existing audit deadline.
  - *Head withholding / rollback:* a friend serves an old head. Newest-wins across all
    reachable friends; undetectable only if every friend you can reach is stale.
  - *Compromised owner machine wipes remote history:* bounded by host-side trash
    (section 5.4).
  - *Relay friend sees traffic metadata* (who talks to whom, volume) for relayed links.
    Content is end-to-end encrypted by libp2p's Noise/TLS.
- **New:** a leaked invite string allows one pairing within 24 h. `friend ls` shows
  every friend, and `friend rm` revokes.

## 9. Migration

hidage has no users yet, so there is no compatibility layer for the network:

- Local repositories (`.hidage/`, the chunk store, the keyring) are unchanged.
- `trust.json` trusted keys are imported into `friends.json` as friends without addresses
  (the user re-runs `friend add <card>` to supply one); blocked keys are dropped.
- `reputation.json` and `accounting.json` are deleted.
- Protocols move to new versions (`push/2.0.0`, new `pair`, `addr`); the DHT ones go.
- The default store moves to `~/.local/share/hidage/store`; an existing `HIDAGE_STORE` or
  `/opt/hidage/store` is still honoured, and `init` writes whichever it finds into the config.

## 10. Implementation plan

Each step is one or a few commits, and the build and tests stay green after each.

1. **Delete stranger features:** `--open`, `--open-peers`, `--transitive`, rendezvous,
   `repquery`, transitive aggregation, and the matching README / DESIGN / THREAT_MODEL
   text. Pure removal.
2. **Config file + friend list:** `config.toml`, `friends.json` (import from
   `trust.json`), `hidage id`, `friend add <card> | rm | ls`, connection gater.
3. **Connectivity:** QUIC, port mapping, AutoNAT, relay service and AutoRelay with
   friends, hole punching; a test with two NATed nodes and one relay node.
4. **Remove the DHT:** direct shard fetch from friends, heads read from friends, address
   records. Delete `dht.go`, the loops, `--bootstrap`.
5. **Hosted sets + push v2:** per-owner layout, quota by bytes held, set replacement,
   trash, lease expiry; delete `internal/accounting` and `internal/reputation` (health
   moves into `friends.json`).
6. **Pairing:** `friend invite`, `/hidage/pair/1.0.0`, pending retries.
7. **Recovery:** sealed roster in the manifest, `restore --from-friends`, adopt.
8. **Daemon:** scheduling, audits, identity-key cache, `passphrase_command`, service units.
9. **CLI consolidation:** `backup`, `restore`, `log`, `forget` merges; remove old names.
10. **Stable packing + retention** (section 5.5).

Steps 1 to 4 already deliver most of the simplification; 5 to 8 deliver the
set-and-forget experience.

## 11. Open questions

1. **Relay bandwidth:** a reachable friend may relay large uploads for others. Cap relayed
   bytes per friend in config, or rely on hole punching plus social norms?
2. **Fixed vs. reciprocal quota:** is a stated `give` per friend enough, or should
   `friend ls` also flag imbalance ("you give Bob 200 GB, he gives you 20 GB")? Reporting
   only, never enforcement, is the proposal.
3. **Identity key at rest:** caching it unencrypted (0600) lets the daemon host after a
   reboot with no prompt; the cost is that a stolen disk exposes the identity (not the
   data). Alternatively, use the OS keyring when available.
4. **Multi-device:** the recovery phrase invites running one identity on a laptop and a
   desktop. Treat each device as its own identity (friends with each other), or support
   shared identity with a merge-safe manifest? This note assumes the former for now.
5. **Tor as an optional transport:** onion addresses derived from the identity key would
   remove NAT and address-book problems entirely, at a large throughput cost. Worth a
   spike after step 4?
