// Package naming turns what a thing was called where it was found into what it
// is called where it is going.
//
// A template is written with braces -- "{host}/{yyyy}/{mm}/{name}" -- and the
// names it may use are fixed and listed in [Vars]. An unknown one is refused
// when the template is parsed rather than expanded into nothing at the first
// carry, since the first carry may be at three in the morning on a machine
// nobody is watching.
package naming

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Vars is everything a template may be written in terms of.
type Vars struct {
	// Host is what the machine carrying the file is called.
	Host string
	// Source is the name of the place it was found.
	Source string
	// Key is what it was called there: a relative path, with slashes.
	Key string
	// Size is in bytes.
	Size int64
	// ModAt is when it was last written. The date parts are taken from it in
	// UTC, so the same file gets the same name whatever the machine's zone is
	// set to -- which on an appliance is often nothing at all.
	ModAt time.Time
	// Digest is "sha256:<hex>", or empty if it has not been computed. A
	// template that asks for it is expanded with it; see [Template.NeedsDigest].
	Digest string
}

// A Template is what an item is called once it is somewhere else.
//
// The zero value expands to the item's key, which is the sensible thing for a
// configuration that said nothing about naming.
type Template struct {
	src   string
	parts []part
}

type part struct {
	// lit is written out as it is. verb is empty when this is one.
	lit string
	// verb is the name inside the braces.
	verb string
}

// verbs is every name a template may use. The map is the documentation: adding
// one here is the whole of adding one.
var verbs = map[string]func(v Vars) string{
	"host":   func(v Vars) string { return v.Host },
	"source": func(v Vars) string { return v.Source },
	"key":    func(v Vars) string { return v.Key },
	"dir":    func(v Vars) string { return dirOf(v.Key) },
	"name":   func(v Vars) string { return path.Base(v.Key) },
	"stem":   func(v Vars) string { return strings.TrimSuffix(path.Base(v.Key), path.Ext(v.Key)) },
	"ext":    func(v Vars) string { return strings.TrimPrefix(path.Ext(v.Key), ".") },
	"size":   func(v Vars) string { return strconv.FormatInt(v.Size, 10) },
	"sha256": func(v Vars) string { return strings.TrimPrefix(v.Digest, "sha256:") },
	"unix":   func(v Vars) string { return strconv.FormatInt(v.ModAt.UTC().Unix(), 10) },
	"yyyy":   func(v Vars) string { return v.ModAt.UTC().Format("2006") },
	"yy":     func(v Vars) string { return v.ModAt.UTC().Format("06") },
	"mm":     func(v Vars) string { return v.ModAt.UTC().Format("01") },
	"dd":     func(v Vars) string { return v.ModAt.UTC().Format("02") },
	"HH":     func(v Vars) string { return v.ModAt.UTC().Format("15") },
	"MM":     func(v Vars) string { return v.ModAt.UTC().Format("04") },
	"SS":     func(v Vars) string { return v.ModAt.UTC().Format("05") },
}

// Names is every verb a template may use, sorted, for an error message and for
// the documentation to be able to say it without repeating it.
func Names() []string {
	ns := make([]string, 0, len(verbs))
	for n := range verbs {
		ns = append(ns, n)
	}
	slices.Sort(ns)

	return ns
}

// Parse reads a template. "{{" is a literal brace; everything else between "{"
// and "}" is a name from [Names].
func Parse(s string) (Template, error) {
	t := Template{src: s}

	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			t.parts = append(t.parts, part{lit: lit.String()})
			lit.Reset()
		}
	}

	for i := 0; i < len(s); {
		c := s[i]
		if c != '{' {
			// "}}" is a closing brace, so that a template that wants both of
			// them writes them the same way.
			if c == '}' && i+1 < len(s) && s[i+1] == '}' {
				lit.WriteByte('}')
				i += 2

				continue
			}

			lit.WriteByte(c)
			i++

			continue
		}
		if i+1 < len(s) && s[i+1] == '{' {
			lit.WriteByte('{')
			i += 2

			continue
		}

		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			return Template{}, fmt.Errorf("a %q at %d is never closed, so there is no name in it to read", "{", i)
		}

		verb := s[i+1 : i+j]
		if _, ok := verbs[verb]; !ok {
			return Template{}, fmt.Errorf("%q is not something a name can be made of; it is one of %s", verb, strings.Join(Names(), ", "))
		}

		flush()
		t.parts = append(t.parts, part{verb: verb})
		i += j + 1
	}
	flush()

	return t, nil
}

// MustParse is [Parse] for a template written in this repository rather than in
// somebody's configuration.
func MustParse(s string) Template {
	t, err := Parse(s)
	if err != nil {
		panic(err)
	}

	return t
}

// String is the template as it was written.
func (t Template) String() string {
	return t.src
}

// NeedsDigest reports whether expanding this template needs the content hash.
//
// It is asked before a carry rather than during one, because a name that
// depends on the hash cannot be known until the file has been read all the way
// through -- so the file is read twice, once to hash and once to send. That is
// a cost the caller should be able to see coming.
func (t Template) NeedsDigest() bool {
	for _, p := range t.parts {
		if p.verb == "sha256" {
			return true
		}
	}

	return false
}

// Expand is the name the item is given.
//
// The result is cleaned the way a path is: no "." or ".." segment survives, no
// segment is empty, and there is no leading slash. A source is trusted to give
// keys that do not collide, but it is not trusted to give keys that stay inside
// the prefix they were meant for.
func (t Template) Expand(v Vars) (string, error) {
	if len(t.parts) == 0 {
		// A configuration that said nothing about naming meant the name it
		// already had.
		return clean(v.Key, v)
	}

	var b strings.Builder
	for _, p := range t.parts {
		if p.verb == "" {
			b.WriteString(p.lit)

			continue
		}
		b.WriteString(verbs[p.verb](v))
	}

	return clean(b.String(), v)
}

func clean(s string, v Vars) (string, error) {
	// A backslash is a separator on the system a file may have come from and a
	// legal character in a name on the one it is going to. Folding it here is
	// what keeps one file from becoming two names.
	s = strings.ReplaceAll(s, "\\", "/")

	segs := make([]string, 0, 8)
	for _, seg := range strings.Split(s, "/") {
		switch seg {
		case "", ".", "..":
			// Dropped rather than resolved: ".." here would mean climbing out
			// of the prefix somebody put this under, and there is no reading of
			// that which is what they meant.
		default:
			segs = append(segs, seg)
		}
	}
	out := strings.Join(segs, "/")
	if out == "" {
		return "", fmt.Errorf("%q leaves nothing to call %q by", s, v.Key)
	}

	return out, nil
}

// dirOf is the directory part of a key, and is empty rather than "." when there
// is none, so that "{dir}/{name}" does not produce "./x".
func dirOf(key string) string {
	d := path.Dir(key)
	if d == "." || d == "/" {
		return ""
	}

	return d
}
