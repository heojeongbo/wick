package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// # The list of modules, and the eight other places it is written down
//
// Adding a module to this repository means editing ten files. Two of those
// edits fail silently if they are forgotten: a missing `cache-dependency-path`
// entry only makes CI slower, and a missing entry in the release tag loop ships
// a module whose tag does not exist. Both have already happened -- see the
// v0.2.1 entry in CHANGELOG.md, which is a release that had to be cut because
// three of its modules could not be built.
//
// So go.work is the truth and everything else is checked against it, here, in
// the gate. Forgetting one is a red build instead of a bad release.
//
// This is not a test of the code. It is a test of the repository, and it is the
// only one of those -- which is why it is at the root, beside the thing it is
// about.
func TestEveryListOfModulesAgrees(t *testing.T) {
	want := useBlock(t)

	// Sanity: the truth has to have something in it, or every check below
	// passes by saying nothing.
	require.New(t).Greater(len(want), 1, "go.work names fewer modules than this repository has")

	t.Run("scripts/test.sh runs each of them", func(t *testing.T) {
		got := between(t, "scripts/test.sh", `readonly MODULES=\(`, `^\)`)
		requireSame(t, want, dotted(got))
	})

	t.Run("the Dockerfile copies each of their go.mod", func(t *testing.T) {
		body := read(t, "Dockerfile")

		var got []string
		for _, m := range submatches(`COPY (\S+)/go\.mod \S+ \./\S+/\n`, body) {
			got = append(got, "./"+m)
		}
		// The root's is on the line above, with go.work beside it.
		require.New(t).Contains(body, "COPY go.work go.mod go.sum ./")
		got = append(got, ".")

		requireSame(t, want, got)
	})

	t.Run("the Dockerfile downloads for each of them", func(t *testing.T) {
		body := read(t, "Dockerfile")

		got := []string{"."}
		for _, m := range submatches(`\(cd (\S+) && go mod download\)`, body) {
			got = append(got, "./"+m)
		}

		requireSame(t, want, got)
	})

	ci := read(t, filepath.Join(".github", "workflows", "ci.yaml"))

	t.Run("every CI loop walks each of them", func(t *testing.T) {
		x := require.New(t)

		loops := submatches(`for m in ([^;]+);`, ci)
		x.Len(loops, 2, "there are two of these, and both have to be right")

		for _, loop := range loops {
			requireSame(t, want, dotted(strings.Fields(loop)))
		}
	})

	t.Run("every CI cache names each of their go.sum", func(t *testing.T) {
		x := require.New(t)

		blocks := submatches(`cache-dependency-path: \|\n((?:\s+\S*go\.sum\n)+)`, ci)
		x.Len(blocks, 2, "a missing entry here does not fail, it only makes the cache useless")

		for _, block := range blocks {
			var got []string
			for _, line := range strings.Fields(block) {
				got = append(got, "./"+strings.TrimSuffix(strings.TrimSuffix(line, "go.sum"), "/"))
			}
			requireSame(t, want, dotted(got))
		}
	})

	t.Run("the release recipe tags each of them", func(t *testing.T) {
		x := require.New(t)

		// `for t in v0.2.1 sink/s3/v0.2.1 ...`
		line := submatches(`for t in (v\S+(?: \S+)*)\n`, read(t, filepath.Join("docs", "EXTENDING.md")))
		x.Len(line, 1, "docs/EXTENDING.md no longer has a tag loop to check")

		var got []string
		for _, tag := range strings.Fields(line[0]) {
			if i := strings.LastIndex(tag, "/v"); i >= 0 {
				got = append(got, "./"+tag[:i])

				continue
			}
			got = append(got, ".")
		}

		requireSame(t, want, got)
	})

	t.Run("each of them is a module", func(t *testing.T) {
		x := require.New(t)

		for _, m := range want {
			x.FileExists(filepath.Join(m, "go.mod"))
		}
	})
}

// useBlock is what go.work says, which is what everything else is measured
// against.
func useBlock(t *testing.T) []string {
	t.Helper()

	return dotted(between(t, "go.work", `^use \(`, `^\)`))
}

// between is the non-empty words inside a block, for the two files that write
// their list as one.
func between(t *testing.T, path, open, close string) []string {
	t.Helper()

	var (
		in  bool
		got []string
	)
	begin := regexp.MustCompile(open)
	end := regexp.MustCompile(close)

	for _, line := range strings.Split(read(t, path), "\n") {
		switch {
		case !in && begin.MatchString(line):
			in = true

		case in && end.MatchString(line):
			return got

		case in:
			if f := strings.Fields(line); len(f) > 0 && !strings.HasPrefix(f[0], "#") {
				got = append(got, f[0])
			}
		}
	}

	t.Fatalf("%s has no %q block, so this check is passing by saying nothing", path, open)

	return nil
}

// dotted normalises "sink/s3" and "./sink/s3" to one spelling, since the eight
// places do not agree about the prefix and there is no reason they should.
func dotted(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSuffix(v, "/")
		if v == "" || v == "." || v == "./" {
			out = append(out, ".")

			continue
		}
		out = append(out, "./"+strings.TrimPrefix(v, "./"))
	}

	return out
}

func submatches(pattern, in string) []string {
	var out []string
	for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(in, -1) {
		out = append(out, m[1])
	}

	return out
}

func read(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	require.NoError(t, err)

	return string(b)
}

func requireSame(t *testing.T, want, got []string) {
	t.Helper()

	w, g := slices.Clone(want), slices.Clone(got)
	slices.Sort(w)
	slices.Sort(g)

	require.New(t).Equal(w, g,
		"this list and go.work disagree about which modules there are")
}
