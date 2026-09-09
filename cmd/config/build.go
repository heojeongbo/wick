package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/z"

	"github.com/heojeongbo/wick/journal"
	"github.com/heojeongbo/wick/journal/bolt"
	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/spool"
)

// A Wick is everything the configuration describes, opened and wired together.
//
// It is one value rather than several return values because the things in it
// have to be let go of together: closing the journal while a spool is still
// carrying is the one arrangement that can lose a record.
type Wick struct {
	Journal journal.Journal
	Group   *spool.Group

	// closers are the sinks that had something to let go of, in the order they
	// were opened.
	closers []sink.Closer
}

// Close lets go of everything this opened, and of nothing it was handed.
func (w *Wick) Close() error {
	var errs []error
	for _, c := range w.closers {
		errs = append(errs, c.Close())
	}
	if w.Journal != nil {
		errs = append(errs, w.Journal.Close())
	}

	return errors.Join(errs...)
}

// Build opens what the configuration describes.
//
// This is where the questions that are about the machine rather than about the
// document are asked -- whether the directory is there, whether the source can
// delete what the retention says to delete. They are asked here, at startup,
// all of them, before anything is carried.
func (c *Config) Build(ctx context.Context) (*Wick, error) {
	w := &Wick{}

	jnl, err := bolt.Open(c.Journal.Path)
	if err != nil {
		return nil, err
	}
	w.Journal = jnl

	// Made once each and shared: two spools that carry to the same sink share
	// its connections, which on a machine with one uplink is the whole point of
	// naming them.
	sinks := make(map[string]sink.Sink, len(c.Sinks))
	for name, sc := range c.Sinks {
		s, err := sc.New(ctx)
		if err != nil {
			_ = w.Close()

			return nil, z.Err(err, "the sink %q", name)
		}

		sinks[name] = s
		if cl, ok := s.(sink.Closer); ok {
			w.closers = append(w.closers, cl)
		}
	}

	l := log.From(ctx)

	spools := make([]*spool.Spool, 0, len(c.Spools))
	for _, sc := range c.Spools {
		s, err := c.spool(ctx, sc, jnl, sinks)
		if err != nil {
			_ = w.Close()

			return nil, err
		}

		spools = append(spools, s)
		l.Info("watching",
			slog.String("spool", sc.Name),
			slog.String("source", sc.Source.Type),
			slog.Int("to", len(sc.To)),
			slog.String("when", fmt.Sprint(sc.When())),
			slog.String("then", fmt.Sprint(sc.Policy())),
		)
	}

	w.Group = spool.NewGroup(spools...)

	return w, nil
}

func (c *Config) spool(ctx context.Context, sc SpoolConfig, jnl journal.Journal, sinks map[string]sink.Sink) (*spool.Spool, error) {
	src, err := sc.Source.Spec.New(ctx)
	if err != nil {
		return nil, z.Err(err, "the source of the spool %q", sc.Name)
	}

	dests := make([]spool.Dest, 0, len(sc.To))
	for _, d := range sc.To {
		dests = append(dests, spool.Dest{
			Name:   d.Sink,
			Sink:   sinks[d.Sink],
			Naming: d.name,
			Verify: d.Verifies(),
		})
	}

	return spool.New(spool.Config{
		Name:     sc.Name,
		Host:     c.Identity.Name,
		Source:   src,
		Dests:    dests,
		Journal:  jnl,
		Trigger:  sc.When(),
		Retain:   sc.Policy(),
		Settle:   sc.Carry.SettleFor,
		Workers:  sc.Carry.Workers,
		Attempts: sc.Carry.Attempts,
	})
}
