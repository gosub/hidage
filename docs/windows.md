# Design note: Windows support

**Status:** approved 2026-10-21, target v0.2.0.
**Scope:** Windows 10 and 11 on amd64 and arm64. The storage format and the network
protocols are unchanged; a backup made on one OS restores on the others.

## 1. Decisions

**A. Hard links stay the deduplication refcount.** NTFS has hard links (up to 1024 per
file, one per folder that shares a chunk) and `golang.org/x/sys/windows` reads the link
count. What breaks is FAT32 and exFAT, on every OS, so `init` and opening the store check
that the store's filesystem supports hard links and refuse with a clear message.

**B. The OS password store holds the passphrase for unattended backups.** The passphrase
can be kept in Windows Credential Manager, the macOS Keychain or the Linux Secret Service
(through `zalando/go-keyring`). `daemon --install` offers to save it there. The lookup
order becomes `HIDAGE_PASSPHRASE`, `passphrase_command`, the password store, the terminal.
`passphrase_command` runs through `cmd /C` on Windows and `sh -c` elsewhere.

**C. `daemon --install` registers a per-user Task Scheduler task** that runs at logon,
like the systemd user unit and the launchd agent, and needs no administrator rights. The
task runs `conhost.exe --headless hidage daemon`, so no console window opens (Windows 10
22H2 and later). The task restarts the daemon if it fails.

**D. Unreadable files are skipped and reported, on every OS.** A file that is locked,
in use or not readable no longer fails the whole backup: it is left out of the snapshot,
like symlinks already are, the backup prints how many, and `status` shows it. On Windows,
cloud placeholders (OneDrive "files on demand" that live only online) are skipped rather
than downloaded in full. Volume Shadow Copy for files that are always locked is left for
later.

**E. Windows 10 and 11, amd64 and arm64.** Release assets `hidage-windows-amd64.exe` and
`hidage-windows-arm64.exe`, unsigned for now (SmartScreen warns on first run).

## 2. Platform fixes

1. **Link count** (`internal/store`): per-OS `linkCount`.
2. **Daemon liveness** (`cmd/hidage`): per-OS `processAlive`; the daemon stops on
   Ctrl+C everywhere and on SIGTERM where it exists.
3. **Locations** (`internal/config`): `%AppData%\hidage` for settings and keys and
   `%LocalAppData%\hidage\store` for data on Windows. Linux and macOS are unchanged, and
   `XDG_CONFIG_HOME` / `XDG_DATA_HOME` still win when set.
4. **Replacing open files**: on Windows a file another process has open cannot be renamed
   over or removed, and antivirus and the indexer open files briefly. Every
   write-temp-then-rename and every removal goes through a helper that retries for a
   short while on sharing violations.
5. **Private folders**: Unix file modes mean nothing on Windows, and a store on another
   drive inherits that drive's permissions, often readable by all users. `init` gives the
   config folder and the store an ACL that only the current user (and SYSTEM) can read.
6. **Portable names on restore**: names a backup can contain but the target OS cannot
   (on Windows: `\ : * ? " < > |`, control characters, trailing dots or spaces, reserved
   names such as `CON`), and names that differ only by case on a case-insensitive
   filesystem, are restored under an escaped name and reported, instead of failing or
   overwriting each other.

## 3. Build and test

- A CI workflow runs vet and the tests on Linux and Windows for every push.
- The release workflow tests on Windows too and adds the two Windows binaries.
- **The Windows binaries are untested on real machines.** Nobody on the project runs
  Windows, and Windows is not emulated locally. The code is cross-compiled and the unit
  tests run on GitHub's Windows runners, but pairing, the firewall prompt, the scheduled
  task, the password store and a real backup and restore have never been tried on
  Windows. The README and the release notes say so.

## 4. Out of scope

Volume Shadow Copy, code signing, a Windows service that runs before logon, NTFS
alternate data streams and ACLs in backups.
