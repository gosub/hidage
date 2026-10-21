package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/multiformats/go-multiaddr"
	"github.com/spf13/cobra"

	"hidage/internal/config"
	"hidage/internal/friends"
	"hidage/internal/keyring"
	"hidage/internal/p2p"
)

// loadFriends opens the friend list, importing a legacy trust.json the first
// time (its trusted keys become friends with the default quota).
func loadFriends() (*friends.Book, error) {
	paths, err := config.Ensure()
	if err != nil {
		return nil, err
	}
	fresh := !friends.Exists(paths.FriendsPath)
	book, err := friends.Load(paths.FriendsPath)
	if err != nil {
		return nil, err
	}
	if fresh {
		settings, _ := config.Load()
		n, err := book.ImportTrust(paths.TrustPath, int64(settings.Hosting.DefaultGive))
		if err != nil {
			return nil, err
		}
		if n > 0 {
			fmt.Fprintf(os.Stderr, "Imported %d trusted key(s) from %s as friends (no addresses yet;\n"+
				"refresh each with `hidage friend add <their card>`).\n", n, paths.TrustPath)
		}
	}
	return book, nil
}

// ownPublicKey returns this node's identity key, unlocking the keyring only if
// it predates the stored public key.
func ownPublicKey() (ed25519.PublicKey, error) {
	paths, err := config.Resolve()
	if err != nil {
		return nil, err
	}
	if !keyring.Exists(paths.KeyringPath) {
		return nil, fmt.Errorf("no keyring found; run `hidage init` (or `hidage init --recover`) first")
	}
	pub, err := keyring.PublicKey(paths.KeyringPath)
	if errors.Is(err, keyring.ErrNoPublicKey) {
		kr, err := unlock()
		if err != nil {
			return nil, err
		}
		return kr.Identity().Public, nil
	}
	return pub, err
}

// nodeState is what a running `serve` publishes about itself for other
// commands (e.g. `hidage id` reporting the addresses actually in use).
type nodeState struct {
	Addrs     []string  `json:"addrs"`
	PID       int       `json:"pid"`
	Locked    bool      `json:"locked,omitempty"` // running without the master key: hosting only, no backups
	UpdatedAt time.Time `json:"updatedAt"`
}

func nodeStatePath() string {
	p, _ := config.Resolve()
	return filepath.Join(p.Dir, "node.json")
}

func writeNodeState(addrs []string, locked bool) error {
	data, err := json.MarshalIndent(nodeState{Addrs: addrs, PID: os.Getpid(), Locked: locked, UpdatedAt: time.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(nodeStatePath(), data, 0o600)
}

// runningNode returns the state of the running daemon, or false if none is
// running (no state file, or its process is gone).
func runningNode() (nodeState, bool) {
	var st nodeState
	data, err := os.ReadFile(nodeStatePath())
	if err != nil {
		return st, false
	}
	if json.Unmarshal(data, &st) != nil || !processAlive(st.PID) {
		return st, false
	}
	return st, true
}

// runningNodeAddrs returns the addresses of the running daemon, or false if
// none is running.
func runningNodeAddrs() ([]string, bool) {
	st, ok := runningNode()
	return st.Addrs, ok
}

// ownCard builds this node's contact card from the running node's addresses, or
// from the configured listen addresses if no node is running.
func ownCard(pub ed25519.PublicKey) (*friends.Card, bool, error) {
	settings, _ := config.Load()
	addrs, running := runningNodeAddrs()
	if !running {
		addrs = p2p.ResolveListenAddrs(settings.Listen)
	}
	return cardFor(pub, addrs), running, nil
}

// cardFor builds a contact card for pub with the given addresses.
func cardFor(pub ed25519.PublicKey, addrs []string) *friends.Card {
	settings, _ := config.Load()
	c := &friends.Card{PubKey: pub, Nickname: settings.Name}
	for _, a := range addrs {
		if ma, err := multiaddr.NewMultiaddr(a); err == nil {
			c.Addrs = append(c.Addrs, ma)
		}
	}
	return c
}

func openInvites() (*friends.Invites, error) {
	paths, err := config.Ensure()
	if err != nil {
		return nil, err
	}
	return friends.OpenInvites(paths.InvitesPath), nil
}

func newIDCmd() *cobra.Command {
	var cardOnly, kit bool
	cmd := &cobra.Command{
		Use:   "id",
		Short: "Show your identity and contact card; --kit prints your recovery kit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pub, err := ownPublicKey()
			if err != nil {
				return err
			}
			card, running, err := ownCard(pub)
			if err != nil {
				return err
			}
			if cardOnly {
				fmt.Println(card.Encode())
				return nil
			}
			if kit {
				return printRecoveryKit()
			}
			pid, _ := friends.PeerIDFromHex(hex.EncodeToString(pub))
			fmt.Printf("Name:        %s\n", card.Nickname)
			fmt.Printf("Public key:  %x\n", pub)
			fmt.Printf("Peer ID:     %s\n", pid)
			if running {
				fmt.Println("Addresses:   (from the running node)")
			} else {
				fmt.Println("Addresses:   (node not running; from `listen` in config.toml)")
			}
			for _, a := range card.Addrs {
				fmt.Printf("  %s\n", a)
			}
			fmt.Println()
			fmt.Println("Contact card (a friend adds you with `hidage friend add <card>`):")
			fmt.Println(card.Encode())
			return nil
		},
	}
	cmd.Flags().BoolVar(&cardOnly, "card", false, "print only the contact card (for scripts)")
	cmd.Flags().BoolVar(&kit, "kit", false, "print your recovery kit: the recovery phrase and your friends' contact cards")
	return cmd
}

// printRecoveryKit prints everything needed to recover on a new machine: the
// recovery phrase and how to reach friends.
func printRecoveryKit() error {
	kr, err := unlock()
	if err != nil {
		return err
	}
	phrase, err := kr.RecoveryCode()
	if err != nil {
		return err
	}
	book, err := loadFriends()
	if err != nil {
		return err
	}
	fmt.Println("hidage recovery kit: print it or keep it offline. Anyone with the phrase can")
	fmt.Println("decrypt all your backups. Re-print it after adding friends.")
	fmt.Println()
	fmt.Println("Recovery phrase:")
	fmt.Println("  " + phrase)
	fmt.Println()
	list := book.List()
	if len(list) == 0 {
		fmt.Println("Friends: none yet. Without a friend's card, recovery needs one of their addresses.")
	} else {
		fmt.Println("Friends (any one is enough to start; the rest are found from your backup):")
		for _, f := range list {
			card := &friends.Card{PubKey: mustPub(f.PubKey), Nickname: f.Nickname}
			for _, a := range f.Addrs {
				if ma, err := multiaddr.NewMultiaddr(a); err == nil {
					card.Addrs = append(card.Addrs, ma)
				}
			}
			fmt.Printf("  %s:\n  %s\n", f.Nickname, card.Encode())
		}
	}
	fmt.Println()
	fmt.Println("On a new machine:")
	fmt.Println("  hidage init --recover            (paste the phrase)")
	fmt.Println("  hidage restore --friends --from <one friend's card above> --to ~/restored")
	return nil
}

func newFriendCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "friend",
		Short: "Manage the friends you back up to and host for",
	}
	cmd.AddCommand(friendInviteCmd(), friendAddCmd(), friendRmCmd(), friendSetCmd(), friendLsCmd())
	return cmd
}

func friendInviteCmd() *cobra.Command {
	var name, give string
	var valid time.Duration
	cmd := &cobra.Command{
		Use:   "invite",
		Short: "Print a single-use invite; whoever adds it becomes your friend (both ways)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pub, err := ownPublicKey()
			if err != nil {
				return err
			}
			settings, _ := config.Load()
			quota := int64(settings.Hosting.DefaultGive)
			if give != "" {
				if quota, err = config.ParseSize(give); err != nil {
					return err
				}
			}
			invites, err := openInvites()
			if err != nil {
				return err
			}
			token, exp, err := invites.Create(name, quota, valid)
			if err != nil {
				return err
			}
			card, running, err := ownCard(pub)
			if err != nil {
				return err
			}
			card.Token, card.Expires = token, exp
			fmt.Println("Send this to your friend; they run `hidage friend add <invite>`:")
			fmt.Println()
			fmt.Println(card.Encode())
			fmt.Println()
			fmt.Printf("It works once, until %s, and gives them %s here.\n", exp.Local().Format("2006-01-02 15:04"), config.FormatSize(quota))
			fmt.Println("Anyone holding it can pair with you, so send it privately.")
			if !running {
				fmt.Println("Your node is not running: start `hidage daemon` so the pairing can complete.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "nickname for the friend who accepts (default: the one they choose)")
	cmd.Flags().StringVar(&give, "give", "", "space to give them, e.g. 200GiB (default: hosting.default_give)")
	cmd.Flags().DurationVar(&valid, "valid", 24*time.Hour, "how long the invite can be used")
	return cmd
}

func friendAddCmd() *cobra.Command {
	var name, give string
	cmd := &cobra.Command{
		Use:   "add <invite | contact-card>",
		Short: "Add a friend from their invite or contact card (or refresh their addresses)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			card, err := friends.DecodeCard(args[0])
			if err != nil {
				return err
			}
			if pub, err := ownPublicKey(); err == nil && pub.Equal(card.PubKey) {
				return errors.New("that is your own contact card")
			}
			settings, _ := config.Load()
			quota := int64(settings.Hosting.DefaultGive)
			if give != "" {
				if quota, err = config.ParseSize(give); err != nil {
					return err
				}
			}
			book, err := loadFriends()
			if err != nil {
				return err
			}
			f, isNew, err := book.Add(card, name, quota)
			if err != nil {
				return err
			}
			if card.IsInvite() {
				return acceptInvite(book, f, card)
			}
			if !isNew {
				fmt.Printf("Updated %s (%s): %d address(es).\n", f.Nickname, f.Short(), len(f.Addrs))
				return nil
			}
			fmt.Printf("Added %s (%s); you give them %s.\n", f.Nickname, f.Short(), config.FormatSize(f.Give))
			fmt.Printf("They must add you too: send them the output of `hidage id --card`.\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "nickname for this friend (default: the one in their card)")
	cmd.Flags().StringVar(&give, "give", "", "space to give them, e.g. 200GiB (default: hosting.default_give)")
	return cmd
}

// acceptInvite presents an invite to the inviter's node, now if our own node
// isn't running (it needs our key), else by leaving it to the running node.
func acceptInvite(book *friends.Book, f *friends.Friend, card *friends.Card) error {
	if time.Now().After(card.Expires) {
		return fmt.Errorf("this invite expired on %s; ask %s for a new one", card.Expires.Local().Format("2006-01-02 15:04"), f.Nickname)
	}
	if err := book.Update(f.PubKey, func(f *friends.Friend) error {
		f.PairToken, f.PairExpires = card.Token, card.Expires
		return nil
	}); err != nil {
		return err
	}
	fmt.Printf("Added %s (%s); you give them %s.\n", f.Nickname, f.Short(), config.FormatSize(f.Give))
	if _, running := runningNodeAddrs(); running {
		fmt.Println("Your running node will now pair with them (see `hidage friend ls`).")
		return nil
	}
	kr, err := unlock()
	if err != nil {
		return err
	}
	node, err := p2p.New(kr.Identity(), nil, nil)
	if err != nil {
		return err
	}
	defer node.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	g, _ := book.Get(f.PubKey)
	if err := presentInvite(ctx, node, book, g); err != nil {
		if errors.Is(err, p2p.ErrPairRefused) {
			return fmt.Errorf("%s's node refused the invite (already used?); ask for a new one", f.Nickname)
		}
		fmt.Printf("Could not reach %s's node yet (%v).\n", f.Nickname, err)
		fmt.Printf("Run `hidage daemon`: it retries until the invite expires (%s). If the invite was\n"+
			"already used, the retries fail too; ask %s for a new one.\n", card.Expires.Local().Format("2006-01-02 15:04"), f.Nickname)
		return nil
	}
	fmt.Printf("Paired: %s now has you as a friend too.\n", f.Nickname)
	return nil
}

func friendRmCmd() *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "rm <friend>",
		Short: "Remove a friend (they can no longer connect or push to you)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			book, err := loadFriends()
			if err != nil {
				return err
			}
			f, err := book.Resolve(args[0])
			if err != nil {
				return err
			}
			if err := book.Remove(f.PubKey); err != nil {
				return err
			}
			fmt.Printf("Removed %s (%s).\n", f.Nickname, f.Short())
			hosted, err := hostedStore()
			if err != nil {
				return err
			}
			live, trash, _ := hosted.Usage(f.PubKey)
			switch {
			case purge:
				if err := hosted.Purge(f.PubKey); err != nil {
					return err
				}
				fmt.Printf("Deleted the %s you held for them.\n", config.FormatSize(live+trash))
			case live+trash > 0:
				settings, _ := config.Load()
				fmt.Printf("The %s you hold for them is deleted when it expires (%d days unrenewed),\n"+
					"or now with `hidage friend rm --purge`.\n", config.FormatSize(live+trash), settings.Hosting.LeaseDays)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete the data you hold for them right away")
	return cmd
}

func friendSetCmd() *cobra.Command {
	var name, give string
	cmd := &cobra.Command{
		Use:   "set <friend>",
		Short: "Change a friend's nickname or the space you give them",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" && give == "" {
				return errors.New("nothing to change: use --name and/or --give")
			}
			var quota int64
			if give != "" {
				var err error
				if quota, err = config.ParseSize(give); err != nil {
					return err
				}
			}
			book, err := loadFriends()
			if err != nil {
				return err
			}
			f, err := book.Resolve(args[0])
			if err != nil {
				return err
			}
			err = book.Update(f.PubKey, func(f *friends.Friend) error {
				if name != "" {
					f.Nickname = name
				}
				if give != "" {
					f.Give = quota
				}
				return nil
			})
			if err != nil {
				return err
			}
			f, _ = book.Get(f.PubKey)
			fmt.Printf("%s (%s): you give them %s.\n", f.Nickname, f.Short(), config.FormatSize(f.Give))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new nickname")
	cmd.Flags().StringVar(&give, "give", "", "space to give them, e.g. 200GiB")
	return cmd
}

func friendLsCmd() *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List your friends: space given and used both ways, audits, reachability",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			book, err := loadFriends()
			if err != nil {
				return err
			}
			list := book.List()
			if len(list) == 0 {
				fmt.Println("(no friends yet; add one with `hidage friend add <their card>`,")
				fmt.Println(" and give them yours from `hidage id --card`)")
				return nil
			}
			hosted, err := hostedStore()
			if err != nil {
				return err
			}
			fmt.Printf("%-16s  %-17s  %-17s  %-24s  %s\n", "NAME", "THEY USE HERE", "YOU USE THERE", "LAST AUDIT", "LAST SEEN")
			for _, f := range list {
				live, _, _ := hosted.Usage(f.PubKey)
				here := config.FormatSize(live) + "/" + config.FormatSize(f.Give)
				there := "-"
				if f.QuotaThere > 0 {
					there = config.FormatSize(f.StoredThere) + "/" + config.FormatSize(f.QuotaThere)
				}
				audit := "never"
				if a := f.LastAudit; a != nil {
					verdict := "ok"
					if !a.OK() {
						verdict = fmt.Sprintf("FAIL %d/%d", a.Passed, a.Sampled)
					}
					audit = verdict + ", " + humanAge(time.Since(a.At)) + " ago"
				}
				seen := "never"
				if !f.LastSeen.IsZero() {
					seen = humanAge(time.Since(f.LastSeen)) + " ago"
				}
				if f.PairingPending() {
					seen = "pairing (their node not reached yet)"
				}
				fmt.Printf("%-16s  %-17s  %-17s  %-24s  %s\n", f.Nickname, here, there, audit, seen)
				if verbose {
					fmt.Printf("    key %s\n", f.PubKey)
					for _, a := range f.Addrs {
						fmt.Printf("    %s\n", a)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "also show keys and addresses")
	return cmd
}
