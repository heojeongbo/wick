package sink_test

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"sync"

	"github.com/heojeongbo/wick/sink"
)

// A whole sink. One method is required; the rest are capabilities the engine
// asks about.
//
// Two things it relies on. "Not there" has to be [fs.ErrNotExist], because
// that is how the engine tells "the write did not stick" from "cannot say" --
// wrap it if you like, [errors.Is] still finds it, but do not replace it. And
// a name that is already taken is replaced rather than refused: in every case
// this is built for, that is the same bytes arriving a second time because the
// first attempt died between the write and the record of it.
type shelf struct {
	mu    sync.Mutex
	items map[string][]byte
}

func (s *shelf) Put(_ context.Context, name string, r io.Reader, want sink.Meta) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	// A stream that ends early looks exactly like a stream that ended, and the
	// only party that can tell is the one that was told how long it should be.
	if want.Size >= 0 && int64(len(b)) != want.Size {
		return fmt.Errorf("%q was to be %d bytes and %d arrived", name, want.Size, len(b))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.items[name] = b

	return nil
}

// Stat makes this a [sink.Stater], which is what a spool told to verify needs.
// Without it, `verify: true` against this sink is refused at startup.
func (s *shelf) Stat(_ context.Context, name string) (sink.Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, ok := s.items[name]
	if !ok {
		return sink.Meta{}, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}

	// No digest: this keeps no hash, so the read-back is of the length. A
	// store that does keep one answers with "sha256:<hex>" and the read-back
	// becomes a comparison of hashes instead.
	return sink.Meta{Size: int64(len(b))}, nil
}

func Example() {
	ctx := context.Background()
	s := &shelf{items: map[string][]byte{}}

	body := []byte("what the robot recorded")
	if err := s.Put(ctx, "thor-top/a.rec", readerOf(body), sink.Meta{Size: int64(len(body))}); err != nil {
		panic(err)
	}

	got, err := s.Stat(ctx, "thor-top/a.rec")
	if err != nil {
		panic(err)
	}
	fmt.Println("it holds", got.Size, "bytes")

	_, err = s.Stat(ctx, "nothing")
	fmt.Println("and about one it does not hold:", err)

	// Output:
	// it holds 23 bytes
	// and about one it does not hold: stat nothing: file does not exist
}

// Registering is done from the init of the package that implements the kind,
// so that importing that package is the whole of making `type: shelf` mean
// something -- and a build that does not import it does not carry whatever it
// depends on either.
func init() { sink.Register("shelf", func() sink.Spec { return &shelfSpec{} }) }

// What a configuration does with a registered kind: name it, read the rest of
// the block into the spec, and make the sink.
func ExampleRegister() {
	spec, err := sink.NewSpec("shelf")
	if err != nil {
		panic(err)
	}

	made, err := spec.New(context.Background())
	if err != nil {
		panic(err)
	}

	fmt.Printf("%T\n", made)

	// Output:
	// *sink_test.shelf
}

// A spec is what a configuration says about a sink before it is one.
//
// [sink.Typed] embedded inline is not optional: a configuration is read
// strictly, so a key nothing answers to is refused -- and without this the one
// key every spec is guaranteed to be handed, `type:`, would be the first thing
// refused.
type shelfSpec struct {
	sink.Typed `yaml:",inline"`

	Where string `yaml:"where"`
}

func (s *shelfSpec) New(context.Context) (sink.Sink, error) {
	return &shelf{items: map[string][]byte{}}, nil
}

// Kinds is what `wick kinds` prints, and what a refusal names -- so a typo in a
// configuration is answered with the list rather than only with the word "no".
func ExampleKinds() {
	// Whatever this build imported, plus the one registered above.
	fmt.Println(slices.Contains(sink.Kinds(), "shelf"))

	_, err := sink.NewSpec("shelves")
	fmt.Println(strings.HasPrefix(err.Error(), `"shelves" is not a kind of sink; it is one of `))

	// Output:
	// true
	// true
}

func readerOf(b []byte) io.Reader { return &bytesReader{b: b} }

type bytesReader struct {
	b []byte
	i int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n

	return n, nil
}
