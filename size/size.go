// Package size is a number of bytes, written the way people write them.
//
// It is its own package because both halves of the program need it: the
// configuration reads "20GiB" out of a file, and the settings of a source or a
// sink hold it. Putting it with the configuration would mean every
// implementation importing the configuration, which is the wrong way round --
// a sink somebody else wrote should need nothing from this repository but the
// interface it implements.
package size

import (
	"fmt"
	"strconv"
	"strings"
)

// Bytes is a number of bytes, written the way people write them.
//
//	20GiB	21474836480
//	20GB	20000000000
//	1024	1024
//
// Both spellings are here because both are meant. A disk is sold in GB and
// reports itself in GiB, and a deployment that writes one and gets the other
// is one that fills up at eight per cent less than it thought.
type Bytes int64

// Int64 is the number, for handing to something that takes one.
func (s Bytes) Int64() int64 { return int64(s) }

var units = []struct {
	suffix string
	scale  int64
}{
	{"KiB", 1 << 10},
	{"MiB", 1 << 20},
	{"GiB", 1 << 30},
	{"TiB", 1 << 40},
	{"KB", 1e3},
	{"MB", 1e6},
	{"GB", 1e9},
	{"TB", 1e12},
	{"B", 1},
}

// Parse reads one of these.
//
// It is here as well as [Bytes.UnmarshalText] because a consumer who is not
// decoding a document has nothing to decode: `go doc ./size` used to render as
// `type Bytes int64`, and the only way to turn "20GiB" into one was to declare
// a variable and call a method on a pointer to it, which is not a thing anybody
// guesses.
func Parse(s string) (Bytes, error) {
	var v Bytes
	if err := v.UnmarshalText([]byte(s)); err != nil {
		return 0, err
	}

	return v, nil
}

// UnmarshalText reads the form written in a configuration file. See [Parse].
func (s *Bytes) UnmarshalText(b []byte) error {
	v := strings.TrimSpace(string(b))
	if v == "" {
		*s = 0

		return nil
	}

	for _, u := range units {
		rest, ok := cutSuffixFold(v, u.suffix)
		if !ok {
			continue
		}

		n, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil {
			return fmt.Errorf("%q is not a number of bytes: %w", v, err)
		}

		*s = Bytes(n * float64(u.scale))

		return nil
	}

	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fmt.Errorf("%q is not a number of bytes; write it as 1024, or as 20MiB, or as 20MB", v)
	}
	*s = Bytes(n)

	return nil
}

// MarshalText writes it back the way it was most likely written, so that
// `wick config` shows a deployment its own settings rather than a number it
// has to work out.
func (s Bytes) MarshalText() ([]byte, error) {
	n := int64(s)
	if n == 0 {
		return []byte("0"), nil
	}

	for _, u := range []struct {
		suffix string
		scale  int64
	}{
		{"TiB", 1 << 40},
		{"GiB", 1 << 30},
		{"MiB", 1 << 20},
		{"KiB", 1 << 10},
	} {
		if n%u.scale == 0 {
			return fmt.Appendf(nil, "%d%s", n/u.scale, u.suffix), nil
		}
	}

	return []byte(strconv.FormatInt(n, 10)), nil
}

// String is what [Bytes.MarshalText] wrote.
func (s Bytes) String() string {
	b, _ := s.MarshalText() //nolint:errcheck // it does not fail

	return string(b)
}

// cutSuffixFold is [strings.CutSuffix] without minding the case, since "20gib"
// is what somebody types.
func cutSuffixFold(s, suffix string) (string, bool) {
	if len(s) < len(suffix) {
		return s, false
	}

	i := len(s) - len(suffix)
	if !strings.EqualFold(s[i:], suffix) {
		return s, false
	}

	return s[:i], true
}
