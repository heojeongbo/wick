# Adding a place to carry to

Two methods and a registration. The contract is a test suite you run against
your implementation rather than a paragraph you have to interpret.

## The interface

```go
type Sink interface {
	Put(ctx context.Context, name string, r io.Reader, want sink.Meta) error
}
```

That is all that is required. `want` is what the caller believes it is sending —
a length and, when it has one, a `sha256:<hex>`. A sink that can check them
does; one that cannot ignores them.

Three more are optional, and the engine asks whether you have them:

```go
type Stater interface{ Stat(ctx, name) (Meta, error) }   // the read-back
type Closer interface{ Close() error }                    // a connection to let go of
```

Without `Stater` there is nothing to confirm a carry with, and a spool
configured `verify: true` against you is refused at startup rather than at the
first carry.

## Two things the engine relies on

**"Not there" must be `fs.ErrNotExist`.** `Stat` answering that a name is not
held is how the engine knows to send it again. Anything else is read as "cannot
say", which is a different decision. Wrap it if you like — `errors.Is` still
finds it — but do not replace it.

**Replace, do not refuse.** A name that is already taken is, in every case this
is built for, the same bytes arriving a second time because the first attempt
died between the write and the record of it.

## The shape of one

```go
package mine

import (
	"context"

	"github.com/heojeongbo/wick/sink"
)

const Kind = "mine"

func init() { sink.Register(Kind, func() sink.Spec { return &Spec{} }) }

// A Spec is what a configuration says about it.
type Spec struct {
	sink.Typed `yaml:",inline"`   // required: see below

	Endpoint string `yaml:"endpoint"`
}

func (s *Spec) New(ctx context.Context) (sink.Sink, error) { ... }
```

`sink.Typed` embedded inline is not optional. A configuration is read strictly —
a key nothing answers to is refused, because that is what a typo looks like —
and without it the one key every spec is guaranteed to be handed (`type:`) would
be the first thing refused.

Registering from `init` means **importing your package is what makes the kind
available**. That is also what lets a build leave one out: `cmd/kinds.go` is the
whole list, and a line removed there plus `go mod tidy` takes the dependency out
of the binary.

## Holding it to the contract

```go
func TestMySink(t *testing.T) {
	sinktest.Suite(t, func(t *testing.T) sink.Sink { return New(...) })
}

// And, if your names become paths rather than opaque keys:
func TestNames(t *testing.T) {
	sinktest.Escapes(t, func(t *testing.T) sink.Sink { return New(...) })
}
```

`Escapes` is separate because it is not true of every sink: an object store's
key is opaque and `..` in one is a key like any other. Asserting it there would
be asserting something false.

There is the same pair for sources (`sourcetest.Suite`) and for journals
(`journaltest.Suite`).

## Testing something with an SDK behind it

Every sink here that talks to a real service does the same two things, and they
answer different questions:

**A narrow interface over the calls you actually make**, with a hand-written
fake. `sink/s3.API` is six calls; `sink/gcs.API` is three. This is where the
failures are tested — a write refused, a read-back that says something else is
there — and it is also documentation: reading it says exactly what is asked of
the other end, which `*storage.Client` does not.

**One test through the real client**, against a transport the test answers.
Both cloud SDKs take one (`option.WithHTTPClient`, `ClientOptions.Transport`),
and a `RoundTripper` is better than a test server here because it *sees every
request the SDK makes* — so the fake is built from what the client actually asks
rather than from a guess at the API.

That second one earns its place. The Azure sink writes its metadata headers in
lower case and reads them back canonicalized; a fake built on a guess would have
agreed with itself and been wrong.

Where a real server can be run in-process, run one: `sink/sftp` starts an actual
SSH server from `x/crypto/ssh` and `sftp.NewServer`, which is what proves the
host key checking refuses a server whose key is not the one written down.

## If it needs a third-party SDK, it needs a module

The AWS, Google and Azure SDKs are some three hundred modules between them.
None of that belongs in the `go.sum` of somebody who imports `spool` to carry
files to a directory, so each of those sinks has its own `go.mod`.

Adding one:

1. `sink/mine/go.mod`, requiring `github.com/heojeongbo/wick` at the current
   release, plus a `replace github.com/heojeongbo/wick => ../..` — the release
   that names your module does not exist yet.
2. The root's `go.mod` requires `github.com/heojeongbo/wick/sink/mine` with a
   matching `replace ... => ./sink/mine`.
3. Add it to `go.work`, `scripts/test.sh` (`MODULES`), the two loops in
   `.github/workflows/ci.yaml`, and the `COPY` lines in the `Dockerfile`.
4. Import it in `cmd/kinds.go`.

## Releasing

**Both replaces come off in the release commit, and every module is tagged.**

The binary imports the sinks and the sinks import the root, which is a cycle
between modules. Pseudo-versions cannot close it: a pseudo-version sorts as a
pre-release of the version it names, so a literal `v0.0.0` outranks every
`v0.0.0-2026...`, and a graph that reaches one recorded `v0.0.0` stops there.
Tags resolve it, because every module can name the same one.

```sh
# In every go.mod: drop the replaces, point each cross-module require at the
# version about to be tagged.
git commit && git push

# One at a time -- `git tag` names one tag and takes the rest as a commit.
for t in v0.2.0 sink/s3/v0.2.0 sink/sftp/v0.2.0 sink/gcs/v0.2.0 sink/azure/v0.2.0
do git tag "$t"; done
git push origin --tags

# Then, in every module: the sums. The public proxy lags the tags by minutes,
# and GOPRIVATE is what fetches from the tag in the meantime -- the hashes are
# the ones the proxy will go on to serve.
export GOPRIVATE='github.com/heojeongbo/*' GOWORK=off
for m in . sink/*; do (cd "$m" && go mod tidy && go build ./...); done
git commit -am 'Record the tagged versions' && git push
```

`tidy` and not `download`: the workspace lets a module compile against a
requirement it never wrote down, because a sibling had it. Outside the
workspace that is a missing `go.sum` entry, and this is the step that finds it —
which is what the `modules` job in CI is checking on every push.

The committed `go.work` is what keeps a checkout from ever caring about any of
this. The `modules` job in CI builds every module with `GOWORK=off`, which is
what says when one of them has fallen behind.

## The gate

```sh
./scripts/test.sh
```

gofmt, vet, tests and a hundred per cent of statements, per module. The reason
for the floor is in the script: this is a thing that deletes files, and every
branch nobody has run is a branch that will first run on a machine holding the
only copy of something.

When a branch cannot be reached through any configuration — an SDK constructor
built never to fail, a syscall that does not error on the machine the tests run
on — the answer here has been a seam: a package-level variable holding the call,
swapped by an internal test, with a comment saying why there was no other way.
There are five of them, and each one says which failure it stands for.
