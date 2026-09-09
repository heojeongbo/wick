// Package registry is how a word in a configuration file becomes a thing.
//
// Sources and sinks both need it and need it identically: `type: dir` has to
// find something that knows what a dir is, without the package holding the
// interface having to know what any particular implementation needs. So it is
// written once, here, rather than twice with the second one drifting.
package registry

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// A Registry maps a name to something that makes an empty T for a decoder to
// read a configuration into.
//
// The zero value is not usable; call [New].
type Registry[T any] struct {
	// noun is what a T is called in an error message: "source", "sink".
	noun string

	mu sync.RWMutex
	fs map[string]func() T
}

func New[T any](noun string) *Registry[T] {
	return &Registry[T]{noun: noun, fs: map[string]func() T{}}
}

// Register adds a kind, and is meant to be called from the init of the package
// that implements it -- so that importing that package is what makes the kind
// available, and a build that does not want a dependency simply does not import
// the thing that needs it.
func (r *Registry[T]) Register(kind string, f func() T) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, dup := r.fs[kind]; dup {
		// A panic, because this can only happen while the program is starting,
		// and two things answering to one name is not a state to run in: which
		// of them a configuration meant would depend on link order.
		panic(fmt.Sprintf("%s: two kinds are called %q", r.noun, kind))
	}
	r.fs[kind] = f
}

// Kinds is every registered name, sorted.
func (r *Registry[T]) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return slices.Sorted(maps.Keys(r.fs))
}

// New makes an empty value of the named kind.
func (r *Registry[T]) New(kind string) (T, error) {
	r.mu.RLock()
	f, ok := r.fs[kind]
	r.mu.RUnlock()

	if !ok {
		var zero T

		return zero, fmt.Errorf("%q is not a kind of %s; it is one of %s", kind, r.noun, strings.Join(r.Kinds(), ", "))
	}

	return f(), nil
}
