package size_test

import (
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/size"
)

// A disk is sold in GB and reports itself in GiB, and a deployment that writes
// one and gets the other is one that fills up at eight per cent less than it
// thought.
func TestReading(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"", 0},
		{"1024", 1024},
		{"512B", 512},
		{"1KiB", 1 << 10},
		{"20MiB", 20 << 20},
		{"2GiB", 2 << 30},
		{"1TiB", 1 << 40},
		{"1KB", 1000},
		{"20MB", 20_000_000},
		{"2GB", 2_000_000_000},
		{"1TB", 1_000_000_000_000},
		{"1.5GiB", 1610612736},
		{" 20 GiB ", 20 << 30},
		{"20gib", 20 << 30},
	} {
		t.Run(tc.in, func(t *testing.T) {
			x := require.New(t)

			var s size.Bytes
			x.NoError(s.UnmarshalText([]byte(tc.in)))
			x.Equal(tc.want, s.Int64())
		})
	}
}

func TestSomethingThatIsNotANumberOfBytes(t *testing.T) {
	x := require.New(t)

	var s size.Bytes
	x.ErrorContains(s.UnmarshalText([]byte("lots")), "write it as 1024")
	x.ErrorContains(s.UnmarshalText([]byte("someGiB")), "is not a number of bytes")
}

// `wick config` shows a deployment its own settings, so a number that was
// written as 20GiB comes back as 20GiB rather than as 21474836480.
func TestWritingItBackOut(t *testing.T) {
	for _, tc := range []struct {
		in   size.Bytes
		want string
	}{
		{0, "0"},
		{1023, "1023"},
		{1 << 10, "1KiB"},
		{20 << 20, "20MiB"},
		{2 << 30, "2GiB"},
		{1 << 40, "1TiB"},
		{1500, "1500"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			x := require.New(t)

			b, err := tc.in.MarshalText()
			x.NoError(err)
			x.Equal(tc.want, string(b))
			x.Equal(tc.want, tc.in.String())
		})
	}
}

func TestItGoesThroughYAMLBothWays(t *testing.T) {
	x := require.New(t)

	var v struct {
		N size.Bytes `yaml:"n"`
	}
	x.NoError(yaml.Unmarshal([]byte("n: 20GiB\n"), &v))
	x.Equal(int64(20<<30), v.N.Int64())

	b, err := yaml.Marshal(v)
	x.NoError(err)
	x.Contains(string(b), "20GiB")
}
