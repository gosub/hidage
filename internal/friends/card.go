package friends

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/multiformats/go-multiaddr"
)

// CardPrefix starts every encoded contact card.
const CardPrefix = "hidage1:"

const cardKind = 1 // payload kind byte: a plain contact card

// checksumSize is the truncated SHA-256 appended to catch copy-paste damage.
const checksumSize = 4

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Card is what a node hands to someone so they can reach it: its public key, a
// suggested nickname and its current addresses. It is not secret.
type Card struct {
	PubKey   ed25519.PublicKey
	Nickname string
	Addrs    []multiaddr.Multiaddr
}

// AddrStrings returns the card's addresses as strings.
func (c *Card) AddrStrings() []string {
	out := make([]string, len(c.Addrs))
	for i, a := range c.Addrs {
		out[i] = a.String()
	}
	return out
}

// Encode renders the card as "hidage1:" + base32 of a compact binary payload
// with a checksum.
//
// Payload: kind byte, 32-byte public key, nickname (uvarint length + bytes),
// address count (uvarint), then each address as uvarint length + multiaddr bytes.
func (c *Card) Encode() string {
	var buf bytes.Buffer
	buf.WriteByte(cardKind)
	buf.Write(c.PubKey)
	writeBytes(&buf, []byte(c.Nickname))
	writeUvarint(&buf, uint64(len(c.Addrs)))
	for _, a := range c.Addrs {
		writeBytes(&buf, a.Bytes())
	}
	sum := sha256.Sum256(buf.Bytes())
	buf.Write(sum[:checksumSize])
	return CardPrefix + strings.ToLower(b32.EncodeToString(buf.Bytes()))
}

// DecodeCard parses an encoded contact card.
func DecodeCard(s string) (*Card, error) {
	s = strings.Join(strings.Fields(s), "") // tolerate line wrapping
	if !strings.HasPrefix(strings.ToLower(s), CardPrefix) {
		return nil, fmt.Errorf("not a hidage contact card (should start with %q)", CardPrefix)
	}
	raw, err := b32.DecodeString(strings.ToUpper(s[len(CardPrefix):]))
	if err != nil {
		return nil, errors.New("contact card is damaged (bad encoding)")
	}
	if len(raw) < 1+ed25519.PublicKeySize+checksumSize {
		return nil, errors.New("contact card is truncated")
	}
	body, sum := raw[:len(raw)-checksumSize], raw[len(raw)-checksumSize:]
	want := sha256.Sum256(body)
	if !bytes.Equal(sum, want[:checksumSize]) {
		return nil, errors.New("contact card is damaged (checksum mismatch); copy it again")
	}
	r := bytes.NewReader(body)
	kind, _ := r.ReadByte()
	if kind != cardKind {
		return nil, fmt.Errorf("unsupported contact card kind %d (newer hidage?)", kind)
	}
	c := &Card{PubKey: make([]byte, ed25519.PublicKeySize)}
	if _, err := io.ReadFull(r, c.PubKey); err != nil {
		return nil, errors.New("contact card is truncated")
	}
	nick, err := readBytes(r)
	if err != nil {
		return nil, err
	}
	c.Nickname = string(nick)
	n, err := binary.ReadUvarint(r)
	if err != nil || n > 64 {
		return nil, errors.New("contact card has a bad address list")
	}
	for i := uint64(0); i < n; i++ {
		b, err := readBytes(r)
		if err != nil {
			return nil, err
		}
		a, err := multiaddr.NewMultiaddrBytes(b)
		if err != nil {
			return nil, fmt.Errorf("contact card has a bad address: %w", err)
		}
		c.Addrs = append(c.Addrs, a)
	}
	return c, nil
}

func writeUvarint(w *bytes.Buffer, v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	w.Write(tmp[:binary.PutUvarint(tmp[:], v)])
}

func writeBytes(w *bytes.Buffer, b []byte) {
	writeUvarint(w, uint64(len(b)))
	w.Write(b)
}

func readBytes(r *bytes.Reader) ([]byte, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil || n > uint64(r.Len()) {
		return nil, errors.New("contact card is truncated")
	}
	b := make([]byte, n)
	io.ReadFull(r, b)
	return b, nil
}
