// Package mem is a source that holds its items in memory.
//
// It is what the tests carry from, and it is also a real source: something that
// produces bytes in-process and wants them carried away has no reason to write
// them to a disk first.
//
// It can also be told to refuse. That is here rather than in a test file
// because refusing is what the engine's interesting paths are about -- a file
// that cannot be opened, a scan that fails halfway, a delete that is denied --
// and a fake that can only succeed leaves all of them unexercised.
package mem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"iter"
	"maps"
	"path"
	"slices"
	"sync"
	"time"

	"github.com/heojeongbo/wick/source"
)

type item struct {
	data  []byte
	modAt time.Time
}

type Source struct {
	mu    sync.Mutex
	items map[string]item
	// chans are the channels handed out by Watch. One per call, so that two
	// callers do not steal each other's wake-ups.
	chans []chan struct{}

	// The refusals. A nil map is a source that refuses nothing.
	failScan   error
	failOpen   map[string]error
	failRemove map[string]error
	failMove   map[string]error
	// failItem is yielded against the named item during a scan, which is the
	// shape of "this one could not be looked at but the rest could".
	failItem map[string]error
}

func New() *Source {
	return &Source{items: map[string]item{}}
}

// Add puts something in, replacing whatever was there under that key.
func (s *Source) Add(key string, data []byte, modAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.items[key] = item{data: data, modAt: modAt}
}

// Keys is what the source holds, sorted.
func (s *Source) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Sorted(maps.Keys(s.items))
}

// Data is what is held under a key.
func (s *Source) Data(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	it, ok := s.items[key]

	return it.data, ok
}

// FailScan makes the next and every scan end with err.
func (s *Source) FailScan(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failScan = err
}

// FailItem makes a scan yield err against key and carry on, which is what one
// unreadable entry among many looks like.
func (s *Source) FailItem(key string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failItem = set(s.failItem, key, err)
}

// FailOpen makes Open refuse for key.
func (s *Source) FailOpen(key string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failOpen = set(s.failOpen, key, err)
}

// FailRemove makes Remove refuse for key.
func (s *Source) FailRemove(key string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failRemove = set(s.failRemove, key, err)
}

// FailMove makes Move refuse for key.
func (s *Source) FailMove(key string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failMove = set(s.failMove, key, err)
}

func set(m map[string]error, k string, v error) map[string]error {
	if m == nil {
		m = map[string]error{}
	}
	m[k] = v

	return m
}

func (s *Source) Scan(ctx context.Context) iter.Seq2[source.Item, error] {
	return func(yield func(source.Item, error) bool) {
		s.mu.Lock()
		fail := s.failScan
		failItem := maps.Clone(s.failItem)
		its := maps.Clone(s.items)
		s.mu.Unlock()

		if fail != nil {
			yield(source.Item{}, fail)

			return
		}

		for _, k := range slices.Sorted(maps.Keys(its)) {
			if err := ctx.Err(); err != nil {
				yield(source.Item{}, err)

				return
			}
			if err, ok := failItem[k]; ok {
				if !yield(source.Item{Key: k}, err) {
					return
				}

				continue
			}

			it := its[k]
			if !yield(source.Item{Key: k, Size: int64(len(it.data)), ModAt: it.modAt}, nil) {
				return
			}
		}
	}
}

func (s *Source) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err, ok := s.failOpen[key]; ok {
		return nil, err
	}

	it, ok := s.items[key]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: key, Err: fs.ErrNotExist}
	}

	return io.NopCloser(bytes.NewReader(it.data)), nil
}

func (s *Source) Remove(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err, ok := s.failRemove[key]; ok {
		return err
	}
	delete(s.items, key)

	return nil
}

func (s *Source) Move(ctx context.Context, key string, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err, ok := s.failMove[key]; ok {
		return err
	}

	it, ok := s.items[key]
	if !ok {
		return &fs.PathError{Op: "move", Path: key, Err: fs.ErrNotExist}
	}

	delete(s.items, key)
	s.items[path.Join(dir, path.Base(key))] = it

	return nil
}

func (s *Source) Watch(ctx context.Context) (<-chan struct{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c := make(chan struct{}, 1)

	s.mu.Lock()
	s.chans = append(s.chans, c)
	s.mu.Unlock()

	go func() {
		<-ctx.Done()

		s.mu.Lock()
		defer s.mu.Unlock()

		if i := slices.Index(s.chans, c); i >= 0 {
			s.chans = slices.Delete(s.chans, i, i+1)
			close(c)
		}
	}()

	return c, nil
}

// Nudge wakes everything watching, the way something appearing would.
func (s *Source) Nudge() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, c := range s.chans {
		select {
		case c <- struct{}{}:
		default:
			// Already has one waiting. A second wake-up says nothing the first
			// one did not.
		}
	}
}

// ErrRefused is what the Fail hooks are given when a test does not care which
// error it is, only that there was one.
var ErrRefused = errors.New("refused")

var (
	_ source.Source  = (*Source)(nil)
	_ source.Remover = (*Source)(nil)
	_ source.Mover   = (*Source)(nil)
	_ source.Watcher = (*Source)(nil)
)
