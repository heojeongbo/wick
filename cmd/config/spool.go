package config

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/heojeongbo/wick/naming"
	"github.com/heojeongbo/wick/retain"
	"github.com/heojeongbo/wick/size"
	"github.com/heojeongbo/wick/source"
	"github.com/heojeongbo/wick/trigger"
)

// A SourceConfig is the place a spool watches. Like a sink, what it holds
// depends on what kind it is.
type SourceConfig struct {
	Type string
	Spec source.Spec
}

func (c *SourceConfig) UnmarshalYAML(b []byte) error {
	var head struct {
		Type string `yaml:"type"`
	}
	if err := yaml.Unmarshal(b, &head); err != nil {
		return fmt.Errorf("read what kind of source this is: %w", err)
	}
	if head.Type == "" {
		return fmt.Errorf("a source has to say what kind it is; it is one of %s", strings.Join(source.Kinds(), ", "))
	}

	spec, err := source.NewSpec(head.Type)
	if err != nil {
		return err
	}
	if err := Decode(b, spec); err != nil {
		return fmt.Errorf("read the settings of the %s source: %w", head.Type, err)
	}

	c.Type = head.Type
	c.Spec = spec

	return nil
}

func (c SourceConfig) MarshalYAML() ([]byte, error) {
	if c.Spec == nil {
		return yaml.Marshal(map[string]string{"type": c.Type})
	}

	// The type is already in there: every spec carries the one that chose it,
	// so that reading is strict without the one key they all get being the
	// first thing refused.
	return yaml.Marshal(c.Spec)
}

// A DestConfig is one of the places a spool carries to.
type DestConfig struct {
	// Sink is the name of a sink declared under `sinks`.
	Sink string `yaml:"sink"`

	// NameAs is what an item is called there. Unset means the name it already
	// had. See [naming.Names] for what may be written in it.
	NameAs string `yaml:"name_as"`

	// Verify says to read the copy back before believing it arrived. It is on
	// unless it is turned off, because the only claim worth recording is the
	// one the store makes when it is asked afterwards.
	Verify *bool `yaml:"verify"`

	// name is NameAs, read.
	name naming.Template
}

// Verifies is whether the copy is read back. Unsaid is yes.
func (c *DestConfig) Verifies() bool { return c.Verify == nil || *c.Verify }

// A TriggerConfig is when to carry. Any of them is enough; whichever comes
// first is the one that fires.
//
// With nothing said, a spool carries only when something asks it to -- which is
// what `wick once` does, and what a watcher does.
type TriggerConfig struct {
	// Every is how long to wait since the last carry. This is the one that
	// makes a quiet machine hand its files over eventually.
	Every time.Duration `yaml:"every"`
	// Count is how many things waiting is enough.
	Count int `yaml:"count"`
	// Bytes is how much waiting is enough, for a source that makes a few very
	// large things rather than many small ones.
	Bytes size.Bytes `yaml:"bytes"`
	// FreeBelow is how little room left is enough. This one is not about
	// carrying; it is about the disk filling, which is what stops the machine
	// doing the thing it is for.
	FreeBelow size.Bytes `yaml:"free_below"`
}

// Trigger is what this says, as one.
func (c TriggerConfig) Trigger() trigger.Trigger {
	var ts []trigger.Trigger
	if c.Every > 0 {
		ts = append(ts, trigger.Every(c.Every))
	}
	if c.Count > 0 {
		ts = append(ts, trigger.Count(c.Count))
	}
	if c.Bytes > 0 {
		ts = append(ts, trigger.Bytes(c.Bytes.Int64()))
	}
	if c.FreeBelow > 0 {
		ts = append(ts, trigger.FreeBelow(uint64(c.FreeBelow))) //nolint:gosec // refused below zero by Evaluate
	}

	return trigger.Any(ts...)
}

// A CarryConfig is how the carrying is done.
type CarryConfig struct {
	// SettleFor is how long something has to have been unchanged before it is
	// carried. A file that is still being written looks exactly like a file
	// that has been written; this is the only thing that tells them apart.
	SettleFor time.Duration `yaml:"settle_for"`
	// Workers is how many things are carried at once. One, unless it is said,
	// because the link is usually shared with something that matters more.
	Workers int `yaml:"workers"`
	// Attempts is how many times a failing item is tried before it is set
	// aside so that it cannot hold up everything behind it.
	Attempts int `yaml:"attempts"`
}

// A RetainConfig is what becomes of the local copy once every destination has
// confirmed it.
//
// What is not said here is `keep`, and that is deliberate: there is no sensible
// default for deleting a file, and a template that guesses is a template that
// deletes something it should not have on a machine nobody was watching.
type RetainConfig struct {
	// After is `keep`, `delete` or `move`.
	After string `yaml:"after"`
	// Grace holds the file for this long after it was carried. The window is
	// long enough that somebody who notices the wrong thing was carried can
	// still reach the original.
	Grace time.Duration `yaml:"grace"`
	// MoveTo is where `move` puts it, taken as it is when it is absolute and
	// relative to the watched directory otherwise.
	MoveTo string `yaml:"move_to"`
	// FreeBelow acts at once when there is less room than this, whatever the
	// grace was going to wait for. The window is a kindness and a disk that is
	// filling is not the time for one.
	FreeBelow size.Bytes `yaml:"free_below"`
}

const (
	retainKeep   = "keep"
	retainDelete = "delete"
	retainMove   = "move"
)

// Policy is what this says, as one.
func (c RetainConfig) Policy() (retain.Policy, error) {
	var act retain.Policy
	switch c.After {
	case "", retainKeep:
		return retain.Keep(), nil

	case retainDelete:
		act = retain.Delete()

	case retainMove:
		if c.MoveTo == "" {
			return nil, fmt.Errorf("`after: move` has nowhere to move to; say `move_to`")
		}
		act = retain.Move(c.MoveTo)

	default:
		return nil, fmt.Errorf("%q is not what becomes of a file; it is %s, %s or %s", c.After, retainKeep, retainDelete, retainMove)
	}

	var ps []retain.Policy
	if c.FreeBelow > 0 {
		ps = append(ps, retain.WhenFreeBelow(uint64(c.FreeBelow), act)) //nolint:gosec // refused below zero by Evaluate
	}
	if c.Grace > 0 {
		ps = append(ps, retain.Grace(c.Grace, act))
	} else {
		ps = append(ps, act)
	}

	if len(ps) == 1 {
		return ps[0], nil
	}

	return retain.First(ps...), nil
}

// A SpoolConfig is one place watched and the places what is found there goes.
type SpoolConfig struct {
	// Name is what it is called, and is what its records are kept under -- so
	// changing it is telling the daemon it has never carried anything.
	Name string `yaml:"name"`

	Source SourceConfig `yaml:"source"`

	// To is everywhere it goes. Something is carried only when every one of
	// them has confirmed it, which is what makes "the cloud and the server in
	// the building" one arrangement rather than two half-arrangements.
	To []DestConfig `yaml:"to"`

	Trigger TriggerConfig `yaml:"trigger"`
	Carry   CarryConfig   `yaml:"carry"`
	Retain  RetainConfig  `yaml:"retain"`

	// what the sections above come to, worked out once when the configuration
	// is read. Kept rather than derived again where they are needed, so that
	// the reasons a policy can be refused are reasons the *configuration* is
	// refused, in one place, and not a branch at every use that nothing takes.
	keep retain.Policy
	when trigger.Trigger
}

// Policy is what becomes of what this spool has carried.
func (c *SpoolConfig) Policy() retain.Policy { return c.keep }

// When is what this spool waits for.
func (c *SpoolConfig) When() trigger.Trigger { return c.when }

// evaluate completes the spool and says whether it is usable as a document.
// Whether the *source* can carry out its retention is a question about the
// source, and is asked when the source is made.
func (c *SpoolConfig) evaluate(sinks map[string]SinkConfig) error {
	if c.Name == "" {
		return fmt.Errorf("a spool has to have a name; it is what its records are kept under")
	}
	if c.Source.Type == "" {
		return fmt.Errorf("the spool %q has no source, so there is nothing for it to carry", c.Name)
	}
	if len(c.To) == 0 {
		return fmt.Errorf("the spool %q has nowhere to carry to", c.Name)
	}

	seen := map[string]bool{}
	for i := range c.To {
		d := &c.To[i]
		switch {
		case d.Sink == "":
			return fmt.Errorf("a destination of the spool %q does not say which sink", c.Name)

		case seen[d.Sink]:
			return fmt.Errorf("the spool %q carries to the sink %q twice", c.Name, d.Sink)
		}
		seen[d.Sink] = true

		if _, ok := sinks[d.Sink]; !ok {
			return fmt.Errorf("the spool %q carries to a sink called %q and nothing under `sinks` is called that", c.Name, d.Sink)
		}

		t, err := naming.Parse(d.NameAs)
		if err != nil {
			return fmt.Errorf("the spool %q cannot name what it carries to %q: %w", c.Name, d.Sink, err)
		}
		d.name = t
	}

	switch {
	case c.Carry.SettleFor < 0:
		return fmt.Errorf("the spool %q would have things sit still for %s, which is not a length of time to wait", c.Name, c.Carry.SettleFor)

	case c.Carry.Workers < 0:
		return fmt.Errorf("the spool %q would carry %d things at once", c.Name, c.Carry.Workers)

	case c.Carry.Attempts < 0:
		return fmt.Errorf("the spool %q would try %d times", c.Name, c.Carry.Attempts)

	case c.Trigger.Every < 0:
		return fmt.Errorf("the spool %q would carry every %s", c.Name, c.Trigger.Every)

	case c.Trigger.Bytes < 0 || c.Trigger.FreeBelow < 0 || c.Retain.FreeBelow < 0:
		return fmt.Errorf("the spool %q gives a number of bytes below zero", c.Name)

	case c.Retain.Grace < 0:
		return fmt.Errorf("the spool %q would hold what it has carried for %s", c.Name, c.Retain.Grace)
	}

	keep, err := c.Retain.Policy()
	if err != nil {
		return fmt.Errorf("the spool %q: %w", c.Name, err)
	}
	c.keep = keep
	c.when = c.Trigger.Trigger()

	return nil
}

var (
	_ yaml.BytesUnmarshaler = (*SourceConfig)(nil)
	_ yaml.BytesMarshaler   = SourceConfig{}
	_ context.Context       = nil
)
