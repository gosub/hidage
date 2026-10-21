package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Size is a byte count written in config and on the command line as a number
// with an optional unit: "100GiB", "1.5TB", "512MiB", "0".
type Size int64

var sizeUnits = []struct {
	suffix string
	mult   float64
}{
	{"tib", 1 << 40}, {"gib", 1 << 30}, {"mib", 1 << 20}, {"kib", 1 << 10},
	{"tb", 1e12}, {"gb", 1e9}, {"mb", 1e6}, {"kb", 1e3},
	{"t", 1 << 40}, {"g", 1 << 30}, {"m", 1 << 20}, {"k", 1 << 10},
	{"b", 1},
}

// ParseSize parses a size such as "100GiB" (binary), "1.5TB" (decimal) or a
// bare byte count. Single-letter units (K, M, G, T) are binary.
func ParseSize(s string) (int64, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	mult := 1.0
	for _, u := range sizeUnits {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSpace(strings.TrimSuffix(t, u.suffix)), u.mult
			break
		}
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid size %q (want e.g. 100GiB, 1.5TB, 512MiB)", s)
	}
	return int64(v * mult), nil
}

// FormatSize renders a byte count with a binary unit, e.g. "100GiB", "1.5TiB".
func FormatSize(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d%s", int64(v), units[i])
	}
	return fmt.Sprintf("%.1f%s", v, units[i])
}

// UnmarshalText lets Size be decoded from a TOML string.
func (s *Size) UnmarshalText(b []byte) error {
	n, err := ParseSize(string(b))
	if err != nil {
		return err
	}
	*s = Size(n)
	return nil
}

// MarshalText renders Size in its human form.
func (s Size) MarshalText() ([]byte, error) { return []byte(FormatSize(int64(s))), nil }

func (s Size) String() string { return FormatSize(int64(s)) }

// Interval is a duration written as "hourly", "daily", "weekly" or a Go
// duration such as "30m" or "6h".
type Interval time.Duration

// UnmarshalText parses an interval.
func (i *Interval) UnmarshalText(b []byte) error {
	switch strings.ToLower(strings.TrimSpace(string(b))) {
	case "hourly":
		*i = Interval(time.Hour)
	case "daily":
		*i = Interval(24 * time.Hour)
	case "weekly":
		*i = Interval(7 * 24 * time.Hour)
	default:
		d, err := time.ParseDuration(strings.TrimSpace(string(b)))
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid interval %q (want hourly, daily, weekly or e.g. 6h)", b)
		}
		*i = Interval(d)
	}
	return nil
}

// MarshalText renders the interval as a Go duration.
func (i Interval) MarshalText() ([]byte, error) { return []byte(i.String()), nil }

func (i Interval) String() string {
	d := time.Duration(i)
	switch {
	case d > 0 && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d > 0 && d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}
