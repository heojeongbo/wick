// Package mem is a sink that keeps what it is given in memory.
//
// It is what the tests carry to, and it can be told to refuse, because refusing
// is what the engine's interesting paths are about: a write that fails, a
// read-back that says something else is there, a store that answers a write
// with a success and then does not hold it.
package mem

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"maps"
	"slices"
	"sync"

	"github.com/heojeongbo/wick/sink"
)

type Sink struct {
	mu   sync.Mutex
	objs map[string][]byte

	failPut  map[string]error
	failStat map[string]error
	// swallow are the names a write is answered for and then not kept, which
	// is the failure verifying exists to catch.
	swallow map[string]bool
	// puts counts the writes that reached the store, so that a test can say
	// "and the second pass sent nothing".
	puts int
}

func New() *Sink {
	return &Sink{objs: map[string][]byte{}}
}

// Names is what the sink holds, sorted.
func (s *Sink) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Sorted(maps.Keys(s.objs))
}

// Data is what is held under a name.
func (s *Sink) Data(name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, ok := s.objs[name]

	return b, ok
}

// Puts is how many writes have reached the store.
func (s *Sink) Puts() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.puts
}

// FailPut makes a write of name refuse.
func (s *Sink) FailPut(name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failPut = set(s.failPut, name, err)
}

// FailStat makes a read-back of name refuse -- which is not the same as saying
// it is not there, and the engine must not read it as such.
func (s *Sink) FailStat(name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failStat = set(s.failStat, name, err)
}

// Swallow makes a write of name succeed and keep nothing. This is the failure
// the read-back exists for: a store that says yes and does not hold it.
func (s *Sink) Swallow(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.swallow == nil {
		s.swallow = map[string]bool{}
	}
	s.swallow[name] = true
}

// Unswallow lets a name be kept again, so that a test can have the second
// attempt be the one that works.
func (s *Sink) Unswallow(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.swallow, name)
	delete(s.failPut, name)
}

func set(m map[string]error, k string, v error) map[string]error {
	if m == nil {
		m = map[string]error{}
	}
	m[k] = v

	return m
}

func (s *Sink) Put(ctx context.Context, name string, r io.Reader, want sink.Meta) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	err, refuse := s.failPut[name]
	s.mu.Unlock()

	if refuse {
		return err
	}

	// Read before the refusal is decided so that a reader which fails part way
	// through is what the caller hears about, the way a real one would.
	b, rerr := io.ReadAll(r)
	if rerr != nil {
		return rerr
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.puts++
	if s.swallow[name] {
		return nil
	}
	s.objs[name] = b

	return nil
}

func (s *Sink) Stat(ctx context.Context, name string) (sink.Meta, error) {
	if err := ctx.Err(); err != nil {
		return sink.Meta{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err, ok := s.failStat[name]; ok {
		return sink.Meta{}, err
	}

	b, ok := s.objs[name]
	if !ok {
		return sink.Meta{}, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}

	sum := sha256.Sum256(b)

	return sink.Meta{Size: int64(len(b)), Digest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

// Reader is what is held under a name, for a test that wants to read it as a
// stream rather than as bytes.
func (s *Sink) Reader(name string) io.Reader {
	b, _ := s.Data(name)

	return bytes.NewReader(b)
}

var (
	_ sink.Sink   = (*Sink)(nil)
	_ sink.Stater = (*Sink)(nil)
)
