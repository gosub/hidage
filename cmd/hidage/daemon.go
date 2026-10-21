package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/friends"
	"hidage/internal/identity"
	"hidage/internal/keyring"
	"hidage/internal/p2p"
	"hidage/internal/repo"
)

func newDaemonCmd() *cobra.Command {
	var listen []string
	var install bool
	cmd := &cobra.Command{
		Use:     "daemon",
		Aliases: []string{"serve"},
		Short:   "Run in the background: back up your folders, host and relay for friends",
		Long: `Run the hidage node: host your friends' data and relay for them, keep in touch
with friends, and (with the keyring unlocked) snapshot and publish the folders listed
in config.toml on schedule, renewing and auditing what friends hold.

The passphrase comes from HIDAGE_PASSPHRASE, passphrase_command in config.toml, or
the terminal. Without it the daemon still hosts for friends (using the cached
identity key) but takes no backups.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if install {
				return installService()
			}
			kr, id, err := daemonKeys()
			if err != nil {
				return err
			}
			settings, _ := config.Load()
			if !cmd.Flags().Changed("listen") {
				listen = settings.Listen
			}
			reach, err := p2p.ParseReachability(settings.Reachability)
			if err != nil {
				return err
			}
			hosted, err := hostedStore()
			if err != nil {
				return err
			}
			book, err := loadFriends()
			if err != nil {
				return err
			}
			isFriend := func(id peer.ID) bool {
				book.Refresh() // pick up `friend add/rm` made while serving
				return book.IsFriendPeer(id)
			}
			invites, err := openInvites()
			if err != nil {
				return err
			}
			// While an invite is open, strangers may connect, but only to pair:
			// every other protocol checks friendship (WithFriends, hosting quota).
			admit := func(id peer.ID) bool { return isFriend(id) || invites.Pending() }
			if reach == p2p.ReachabilityAuto && !hasPublicInterface() {
				// No public IP on any interface: behind a NAT unless a port is
				// forwarded (then set reachability = "public"). Deciding now, rather
				// than waiting for AutoNAT, which needs several peers to agree,
				// gets relay reservations in place even in a two-friend group.
				reach = p2p.ReachabilityPrivate
			}
			node, err := p2p.New(id, nil, listen,
				p2p.WithInboundFilter(admit), // friends only (and invitees while an invite is open)
				p2p.WithFriends(isFriend),
				p2p.WithRelayService(isFriend), // relay for friends while reachable
				p2p.WithRelayCandidates(func() []peer.AddrInfo { return friendAddrInfos(book) }),
				p2p.WithReachability(reach))
			if err != nil {
				return err
			}
			defer node.Close()

			ctx := context.Background()
			heads, err := headsStore()
			if err != nil {
				return err
			}
			node.EnableHeadStore(heads.Put, heads.Get) // owner-signed; returned to the owner on recovery
			node.EnableAddrService(func(subject string) *p2p.AddrRecord { return storedAddrRecord(book, subject) })
			node.EnablePairing(func(from peer.ID, token []byte, cardStr string) (string, bool) {
				card, err := friends.DecodeCard(cardStr)
				if err != nil {
					return "", false
				}
				if pid, err := friends.PeerIDFromHex(hex.EncodeToString(card.PubKey)); err != nil || pid != from {
					return "", false // the card must be the requester's own
				}
				inv, ok := invites.Consume(token)
				if !ok {
					return "", false
				}
				card.Token = nil
				f, _, err := book.Add(card, inv.Nickname, inv.Give)
				if err != nil {
					return "", false
				}
				fmt.Printf("[friends] %s accepted your invite and is now a friend\n", f.Nickname)
				return cardFor(id.Public, node.ListenAddrs()).Encode(), true
			})
			node.EnableHosting(hosted,
				func(ownerHex string) int64 {
					f, ok := book.Get(ownerHex)
					if !ok {
						return 0 // friends only
					}
					return f.Give
				},
				func(ownerHex string, held int64) {
					book.Update(ownerHex, func(f *friends.Friend) error { f.LastSeen = time.Now().UTC(); return nil })
				})
			go keepInTouchLoop(ctx, node, book)
			go reapLoop(ctx, hosted, time.Duration(settings.Hosting.LeaseDays)*24*time.Hour,
				time.Duration(settings.Hosting.TrashDays)*24*time.Hour)

			if err := writeNodeState(node.ListenAddrs(), kr == nil); err != nil {
				return err
			}
			defer os.Remove(nodeStatePath())
			go nodeStateLoop(ctx, node, kr == nil) // relay and port-mapped addresses appear later
			if kr != nil {
				go backupLoop(ctx, node, kr, book, settings)
			}
			fmt.Println("hidage node listening; addresses:")
			for _, a := range node.Addrs() {
				fmt.Printf("  %s\n", a)
			}
			fmt.Println("Friends add you with the card from `hidage id --card`.")
			switch n := len(settings.Backup.Folders); {
			case kr == nil:
				fmt.Println("Keyring locked: hosting for friends only, no backups (set passphrase_command).")
			case n == 0:
				fmt.Println("No folders to back up yet (add them to `folders` in config.toml).")
			default:
				fmt.Printf("Backing up %d folder(s) every %s.\n", n, settings.Backup.Every)
			}
			fmt.Println("Ctrl-C to stop.")
			waitForSignal()
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&listen, "listen", nil, "listen multiaddr(s) (default: `listen` in config.toml)")
	cmd.Flags().BoolVar(&install, "install", false, "install a user service (systemd or launchd) that runs the daemon at login")
	return cmd
}

// daemonKeys unlocks the keyring if a passphrase is available, caching the
// identity key for later locked starts. Without one it falls back to the cached
// identity (kr is then nil: hosting only).
func daemonKeys() (*keyring.Keyring, identity.Identity, error) {
	paths, err := config.Resolve()
	if err != nil {
		return nil, identity.Identity{}, err
	}
	if !keyring.Exists(paths.KeyringPath) {
		return nil, identity.Identity{}, fmt.Errorf("no keyring found; run `hidage init` (or `hidage init --recover`) first")
	}
	pass, perr := readPassphrase(false)
	if perr == nil {
		kr, err := keyring.Load(paths.KeyringPath, pass)
		if err != nil {
			return nil, identity.Identity{}, err
		}
		if err := kr.SaveIdentity(paths.IdentityPath); err != nil {
			return nil, identity.Identity{}, err
		}
		return kr, kr.Identity(), nil
	}
	id, err := keyring.LoadIdentity(paths.IdentityPath)
	if err != nil {
		return nil, identity.Identity{}, fmt.Errorf("cannot unlock the keyring (%v), and no cached identity yet: "+
			"set passphrase_command in config.toml, or run `hidage daemon` once from a terminal", perr)
	}
	return nil, id, nil
}

// backupLoop runs scheduled backups of the configured folders until ctx ends.
func backupLoop(ctx context.Context, node *p2p.Node, kr *keyring.Keyring, book *friends.Book, settings config.Settings) {
	folders := settings.Backup.FolderPaths()
	if len(folders) == 0 {
		return
	}
	// Give connections to friends (and relay reservations) a moment first.
	select {
	case <-ctx.Done():
		return
	case <-time.After(20 * time.Second):
	}
	for {
		for _, folder := range folders {
			scheduledBackup(ctx, node, kr, book, settings, folder)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(settings.Backup.Every)):
		}
	}
}

// scheduledBackup snapshots one folder if it changed, publishes it if it
// changed or its publish is due for renewal, and audits friends if due.
func scheduledBackup(ctx context.Context, node *p2p.Node, kr *keyring.Keyring, book *friends.Book, settings config.Settings, folder string) {
	out := &prefixWriter{prefix: "[" + filepath.Base(folder) + "] ", w: os.Stdout}
	r, err := openOrInit(folder, kr)
	if err != nil {
		fmt.Fprintf(out, "%v\n", err)
		return
	}
	snap, stats, err := r.SnapshotIfChanged(kr.Identity().Private)
	if err != nil {
		fmt.Fprintf(out, "snapshot failed: %v\n", err)
		return
	}
	if !stats.Unchanged {
		fmt.Fprintf(out, "snapshot %s: %d new objects\n", snap.ID[:12], stats.NewObjects)
	}
	dist, derr := readDistribution(r.HidageFile("distribution.json"))
	due := derr != nil || dist.SnapshotID != snap.ID ||
		time.Since(dist.PublishedAt) > time.Duration(settings.Backup.RenewEvery)
	if due {
		c, cancel := context.WithTimeout(ctx, 30*time.Minute)
		opts := publishOpts{redundancy: settings.Backup.Redundancy, minPeers: settings.Backup.MinFriends, quiet: true}
		if err := publishRepo(c, node, kr, r, book, opts, out); err != nil {
			fmt.Fprintf(out, "publish failed: %v\n", err)
		}
		cancel()
		dist, derr = readDistribution(r.HidageFile("distribution.json"))
	}
	if derr == nil && auditDue(dist, book, time.Duration(settings.Backup.AuditEvery)) {
		c, cancel := context.WithTimeout(ctx, 5*time.Minute)
		if err := auditRepo(c, node, r, book, nil, true, out); err != nil {
			fmt.Fprintf(out, "audit failed: %v\n", err)
		}
		cancel()
	}
}

// openOrInit opens the repository rooted at folder, creating it if needed.
func openOrInit(folder string, kr *keyring.Keyring) (*repo.Repo, error) {
	if repo.Exists(folder) {
		return repo.Open(folder, kr.MasterKey(), config.DataStoreDir())
	}
	if _, err := os.Stat(folder); err != nil {
		return nil, fmt.Errorf("cannot back up %s: %w", folder, err)
	}
	return repo.Init(folder, kr.MasterKey(), config.DataStoreDir())
}

// auditDue reports whether any friend holding shards has not been audited
// within every.
func auditDue(dist distribution, book *friends.Book, every time.Duration) bool {
	for pubHex := range dist.Peers {
		f, ok := book.Get(pubHex)
		if ok && (f.LastAudit == nil || time.Since(f.LastAudit.At) > every) {
			return true
		}
	}
	return false
}

// prefixWriter prefixes every line written through it (for the daemon's log).
type prefixWriter struct {
	prefix string
	w      io.Writer
	buf    bytes.Buffer
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.buf.Write(b)
	for {
		line, err := p.buf.ReadString('\n')
		if err != nil {
			p.buf.WriteString(line) // incomplete line: keep for the next write
			return len(b), nil
		}
		if _, err := io.WriteString(p.w, p.prefix+strings.TrimLeft(line, " ")); err != nil {
			return 0, err
		}
	}
}

// installService writes a user service that runs `hidage daemon` at login.
func installService() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	settings, _ := config.Load()
	if settings.PassphraseCommand == "" {
		fmt.Println("Note: passphrase_command is not set in config.toml, so after a reboot the service")
		fmt.Println("hosts for friends but takes no backups until you set it.")
	}
	switch runtime.GOOS {
	case "linux":
		p := filepath.Join(home, ".config", "systemd", "user", "hidage.service")
		unit := fmt.Sprintf(`[Unit]
Description=hidage: backups with friends

[Service]
ExecStart=%s daemon
Restart=on-failure
RestartSec=30

[Install]
WantedBy=default.target
`, exe)
		if err := writeNew(p, unit); err != nil {
			return err
		}
		fmt.Printf("Wrote %s. Start it now and at every login with:\n", p)
		fmt.Println("  systemctl --user daemon-reload && systemctl --user enable --now hidage")
		fmt.Println("To keep it running while you are logged out: loginctl enable-linger $USER")
	case "darwin":
		p := filepath.Join(home, "Library", "LaunchAgents", "org.hidage.daemon.plist")
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>org.hidage.daemon</string>
  <key>ProgramArguments</key><array><string>%s</string><string>daemon</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, exe, filepath.Join(home, "Library", "Logs", "hidage.log"), filepath.Join(home, "Library", "Logs", "hidage.log"))
		if err := writeNew(p, plist); err != nil {
			return err
		}
		fmt.Printf("Wrote %s. Start it with:\n  launchctl load -w %s\n", p, p)
	default:
		return fmt.Errorf("no service template for %s; run `%s daemon` from your init system", runtime.GOOS, exe)
	}
	return nil
}

// writeNew writes a file, refusing to overwrite one that differs.
func writeNew(p, content string) error {
	if old, err := os.ReadFile(p); err == nil {
		if string(old) == content {
			return nil
		}
		return fmt.Errorf("%s already exists and differs; remove it first", p)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}
