package config

import (
	"reflect"
	"slices"

	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/source"
)

// Tag is the struct tag a spec puts on a field that must not be printed:
//
//	SecretAccessKey string `yaml:"secret_access_key" wick:"secret"`
//
// It is a tag rather than a list kept here because the field and the fact that
// it is a secret belong in the same place. A sink somebody else wrote becomes
// available by being imported, and its password has to be covered by the same
// rule without this package having heard of it.
const (
	Tag = "wick"

	// Secret is the value of [Tag] that means "never print this".
	Secret = "secret"

	// Redacted is what is printed instead. It says that there is one, because
	// "" would say the opposite and sending somebody to look for a password
	// that is already set is its own afternoon.
	Redacted = "(set)"
)

// Redact is c with every secret replaced by [Redacted].
//
// `wick config` is the command this program's own error messages tell people to
// run, and its output is what gets pasted into an issue. Printing an access key
// there is a decision, so it is one somebody has to make out loud with
// --reveal.
//
// The copy is deep enough to reach every spec and no deeper. Nothing shares a
// spec today -- config prints and exits -- but a shallow copy would mean that
// the day something builds a sink after printing one, it authenticates with the
// word "(set)".
func Redact(c *Config) *Config {
	out := *c

	if c.Sinks != nil {
		out.Sinks = make(map[string]SinkConfig, len(c.Sinks))
		for name, sc := range c.Sinks {
			if s, ok := redacted(sc.Spec).(sink.Spec); ok {
				sc.Spec = s
			}
			out.Sinks[name] = sc
		}
	}

	out.Spools = slices.Clone(c.Spools)
	for i := range out.Spools {
		if s, ok := redacted(out.Spools[i].Source.Spec).(source.Spec); ok {
			out.Spools[i].Source.Spec = s
		}
	}

	return &out
}

// redacted is a copy of v with its secrets covered, or v itself when there is
// nothing it can do with it.
//
// Answering with the original rather than refusing is deliberate: this is on
// the path of a command whose whole job is to print, and a spec shaped in a way
// this did not expect should print unchanged rather than stop the command. What
// it cannot be is *wrong* -- so a field is only touched when it says it is a
// secret.
func redacted(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return v
	}

	cp := reflect.New(rv.Elem().Type())
	cp.Elem().Set(rv.Elem())
	cover(cp.Elem())

	return cp.Interface()
}

// cover walks a struct and blanks what is tagged, following embedded structs so
// that a spec which keeps its credentials in one is covered too.
func cover(rv reflect.Value) {
	t := rv.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}

		fv := rv.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			cover(fv)

			continue
		}

		if f.Tag.Get(Tag) != Secret {
			continue
		}
		if f.Type.Kind() == reflect.String && fv.String() != "" {
			fv.SetString(Redacted)
		}
	}
}
