package naming_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/naming"
)

// vars is one item, fully described, so that a test about naming does not also
// have to be about where the values came from.
func vars() naming.Vars {
	return naming.Vars{
		Host:   "thor-top",
		Source: "recordings",
		Key:    "2026/session_0912.rec",
		Size:   1234,
		ModAt:  time.Date(2026, 9, 12, 3, 4, 5, 0, time.UTC),
		Digest: "sha256:abc123",
	}
}

func TestNames(t *testing.T) {
	x := require.New(t)

	ns := naming.Names()
	x.Contains(ns, "host")
	x.Contains(ns, "sha256")
	x.Equal(ns, append([]string(nil), ns...))
	x.IsIncreasing(ns)
}

func TestParse(t *testing.T) {
	t.Run("a name that nothing answers to is refused when it is written", func(t *testing.T) {
		x := require.New(t)

		_, err := naming.Parse("{host}/{nope}")
		x.ErrorContains(err, `"nope"`)
		// The message says what could have been meant instead.
		x.ErrorContains(err, "host")
	})
	t.Run("a brace that never closes is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := naming.Parse("{host")
		x.ErrorContains(err, "never closed")
	})
	t.Run("a doubled brace is a brace", func(t *testing.T) {
		x := require.New(t)

		tp, err := naming.Parse("{{literal}}/{name}")
		x.NoError(err)

		s, err := tp.Expand(vars())
		x.NoError(err)
		x.Equal("{literal}/session_0912.rec", s)
	})
	t.Run("the template remembers how it was written", func(t *testing.T) {
		x := require.New(t)

		tp, err := naming.Parse("{host}/{name}")
		x.NoError(err)
		x.Equal("{host}/{name}", tp.String())
	})
}

func TestMustParse(t *testing.T) {
	t.Run("what parses is returned", func(t *testing.T) {
		x := require.New(t)

		x.Equal("{name}", naming.MustParse("{name}").String())
	})
	t.Run("what does not is a panic, since it was written here and not by a deployment", func(t *testing.T) {
		x := require.New(t)

		x.Panics(func() { naming.MustParse("{nope}") })
	})
}

func TestNeedsDigest(t *testing.T) {
	t.Run("a template that asks for the hash says so", func(t *testing.T) {
		x := require.New(t)

		x.True(naming.MustParse("blob/{sha256}").NeedsDigest())
	})
	t.Run("and one that does not, does not", func(t *testing.T) {
		x := require.New(t)

		x.False(naming.MustParse("{host}/{name}").NeedsDigest())
		x.False(naming.Template{}.NeedsDigest())
	})
}

func TestExpand(t *testing.T) {
	t.Run("every name is what it says it is", func(t *testing.T) {
		x := require.New(t)

		for _, tc := range []struct{ tmpl, want string }{
			{"{host}", "thor-top"},
			{"{source}", "recordings"},
			{"{key}", "2026/session_0912.rec"},
			{"{dir}", "2026"},
			{"{name}", "session_0912.rec"},
			{"{stem}", "session_0912"},
			{"{ext}", "rec"},
			{"{size}", "1234"},
			{"{sha256}", "abc123"},
			{"{unix}", "1789182245"},
			{"{yyyy}", "2026"},
			{"{yy}", "26"},
			{"{mm}", "09"},
			{"{dd}", "12"},
			{"{HH}", "03"},
			{"{MM}", "04"},
			{"{SS}", "05"},
		} {
			t.Run(tc.tmpl, func(t *testing.T) {
				x := require.New(t)

				s, err := naming.MustParse(tc.tmpl).Expand(vars())
				x.NoError(err)
				x.Equal(tc.want, s)
			})
		}
		x.Len(naming.Names(), 17)
	})
	t.Run("the parts are joined with whatever was written between them", func(t *testing.T) {
		x := require.New(t)

		s, err := naming.MustParse("{host}/{yyyy}/{mm}/{dd}/{stem}-{size}.{ext}").Expand(vars())
		x.NoError(err)
		x.Equal("thor-top/2026/09/12/session_0912-1234.rec", s)
	})
	t.Run("the dates are read in UTC, whatever the machine is set to", func(t *testing.T) {
		x := require.New(t)

		v := vars()
		// The same instant, said in a zone that is a day ahead of it.
		v.ModAt = v.ModAt.In(time.FixedZone("KST", 9*60*60))

		s, err := naming.MustParse("{yyyy}{mm}{dd}T{HH}{MM}{SS}").Expand(v)
		x.NoError(err)
		x.Equal("20260912T030405", s)
	})
	t.Run("a template that says nothing means the name it already had", func(t *testing.T) {
		x := require.New(t)

		s, err := naming.Template{}.Expand(vars())
		x.NoError(err)
		x.Equal("2026/session_0912.rec", s)
	})
	t.Run("a key with no directory in it leaves no empty segment", func(t *testing.T) {
		x := require.New(t)

		v := vars()
		v.Key = "session.rec"

		s, err := naming.MustParse("{host}/{dir}/{name}").Expand(v)
		x.NoError(err)
		x.Equal("thor-top/session.rec", s)
	})
	t.Run("nothing climbs out of the prefix it was put under", func(t *testing.T) {
		x := require.New(t)

		v := vars()
		v.Key = "../../etc/passwd"

		s, err := naming.MustParse("safe/{key}").Expand(v)
		x.NoError(err)
		x.Equal("safe/etc/passwd", s)
	})
	t.Run("a separator from another system is folded into this one", func(t *testing.T) {
		x := require.New(t)

		v := vars()
		v.Key = `logs\a.rec`

		s, err := naming.MustParse("{key}").Expand(v)
		x.NoError(err)
		x.Equal("logs/a.rec", s)
	})
	t.Run("a name that comes out empty is refused rather than carried", func(t *testing.T) {
		x := require.New(t)

		v := vars()
		v.Key = "a.rec" // no directory, so "{dir}" is nothing

		_, err := naming.MustParse("{dir}").Expand(v)
		x.ErrorContains(err, "leaves nothing to call")
		x.ErrorContains(err, "a.rec")
	})
	t.Run("a key that is only a slash leaves nothing either", func(t *testing.T) {
		x := require.New(t)

		v := vars()
		v.Key = "/"

		_, err := naming.Template{}.Expand(v)
		x.ErrorContains(err, "leaves nothing to call")
	})
}
