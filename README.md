# wick

Files pile up on machines nobody logs into. `wick` watches the places they pile
up in, waits for one to stop being written, carries it to everywhere it is meant
to go, proves the copy arrived by reading it back, and only then does whatever
the deployment said to do with the original.

The name is the mechanism. A wick moves liquid continuously by capillary action:
no pump, no command, it just draws.

```sh
$ wick once
recordings: 3 carried, 1.2 GiB, 3 retired, 0 set aside, 0 waiting
```

## What it is for

A machine that produces large files on a schedule nobody controls, on a link
nobody wants to saturate, with a disk that fills. An onboard computer writing
session recordings; a camera writing video; a build agent writing artefacts.
The daemon is the thing that was always going to be written by hand, badly, as a
cron job that eventually deletes something it should not have.

| | |
| --- | --- |
| **Somewhere to carry from** | a directory, watched or scanned or both |
| **Somewhere to carry to** | S3, Google, Azure, an HTTP or WebDAV server, a machine over SSH, or another directory. Several at once |
| **When** | an interval, a count, a size, or the disk getting tight -- whichever comes first |
| **Then** | keep it, delete it, delete it after a while, or move it somewhere else |

## The rule everything else follows from

> A false "not carried" costs one upload. A false "carried" loses the file.

So nothing is written to the journal before the copy has been read back and
confirmed, and the local file is not touched until the record exists. A crash at
any point costs bytes and never a file; see the table in `spool/spool.go`.

Two things fall out of that and are worth knowing before reading the code:

**Every destination has to answer.** A spool with two sinks carries to both, and
the file is only *carried* when both have confirmed it. A restart in the middle
of a fan-out does not send again to the one that already had it -- on a metered
link that is the difference between a retry and a bill.

**Nothing is carried until the trigger says so.** `wick run` wakes on a nudge or
a timer, looks, and asks; only `wick once` skips the question, because whoever
ran it has already answered it. Waking is a scan and carrying is the link this
machine shares with whatever it is really for, so the two are not the same
decision.

**A file is not eligible until it has stopped changing.** A file that is still
being written looks exactly like a file that has been written. `settle_for` is
the only thing that tells them apart, and it is why the filesystem watcher is
allowed to be wrong: it only ever says "look again", and the scan is what is
true.

## Getting started

```sh
$ wick kinds           # what this build can carry from, and to
$ wick config          # what it thinks it has been told
$ wick check           # whether that would actually work on this machine
$ wick once            # carry what is due, once
$ wick status          # what the journal knows
$ wick run             # watch, and carry what turns up
```

The first three move nothing. `wick check` is the one worth knowing about: it
opens everything the configuration describes and then asks each destination
whether it holds a name nothing is called, which is how a bucket that is not
there, a role that is not allowed and a `known_hosts` that is missing all get
found before the daemon is restarted rather than at three in the morning.

`wick --help`, and `--help` on any of them, says the rest. Secrets are printed
as `(set)` unless `--reveal` says otherwise, because `wick config` output is
what gets pasted into a bug report.

`wick.yaml` is the configuration and is also the documentation: every key in it
carries a comment saying what it does and when to reach for it. What it says can
be overruled, and the last word wins:

```
built-in defaults  <  the file  <  the environment  <  the flags
```

Every field answers to a variable named after the path to it -- `path` of
`journal` is `WICK_JOURNAL_PATH` -- and a value in the file can name a variable
instead of holding one, which is what a password is for:

```yaml
access_key_id: "${env:S3_KEY_ID}"
```

### The smallest thing that does something

```yaml
journal:
  path: /var/lib/wick/wick.db

sinks:
  cloud:
    type: s3
    bucket: recordings
    endpoint: https://s3.example.com

spools:
  - name: recordings
    source:
      type: dir
      path: /var/log/app/rec
      include: ["*.rec"]
    carry:
      settle_for: 10s
    to:
      - sink: cloud
        name_as: "{host}/{yyyy}/{mm}/{dd}/{name}"
    trigger:
      every: 15m
      free_below: 20GiB
    retain:
      after: delete
      grace: 24h
```

`{host}` is what keeps three machines writing into one bucket from writing over
each other. It is the hostname unless `identity.name` says otherwise.

## Using it as a library

The binary is one arrangement of the packages, not the only one. A spool is one
source, any number of destinations, and a journal:

```go
s, err := spool.New(spool.Config{
	Name:    "recordings",
	Host:    "thor-top",
	Source:  src,      // source.Source
	Journal: jnl,      // journal.Journal
	Dests: []spool.Dest{
		{Name: "cloud", Sink: cloud, Naming: naming.MustParse("{host}/{name}"), Verify: true},
		{Name: "onsite", Sink: onsite, Naming: naming.MustParse("{name}"), Verify: true},
	},
	Trigger: trigger.Any(trigger.Every(15*time.Minute), trigger.FreeBelow(20<<30)),
	Retain:  retain.Grace(24*time.Hour, retain.Delete()),
	Settle:  10 * time.Second,
})
if err != nil {
	return err
}

report, err := s.Once(ctx)   // or s.Run(ctx) to keep going
```

| | |
| --- | --- |
| `source` | where things accumulate. Two methods, plus `Remover`, `Mover`, `Watcher` and `Rooted` for what an implementation can also do |
| `sink` | where they go. One method, plus `Stater` for reading one back |
| `journal` | what has already gone |
| `trigger` | when to go: `Every`, `Count`, `Bytes`, `FreeBelow`, `Any`, `All` |
| `retain` | what becomes of the original: `Keep`, `Delete`, `Grace`, `Move`, `WhenFreeBelow`, `First` |
| `naming` | what a thing is called once it is somewhere else |
| `spool` | the engine |

Each of those has runnable examples on
[pkg.go.dev](https://pkg.go.dev/github.com/heojeongbo/wick). They are compiled
and run by `go test`, so unlike a block in a README they cannot quietly stop
being true.

One thing to know before writing a `Dest` by hand: `Verify` is false by default
in Go and true by default in YAML, so a spool built in code carries without
reading anything back unless it is asked to. Set it.

Adding a kind of sink is implementing one method and calling `sink.Register`
from your package's `init`. Importing your package is then what makes
`type: yours` mean something -- see `cmd/kinds.go`, which is the whole of what
this build can do. There are conformance suites for sources, sinks and
journals, so an implementation can be held to the contract rather than to the
doc comments:

```go
func TestMySink(t *testing.T) {
	sinktest.Suite(t, func(t *testing.T) sink.Sink { return New(...) })
}
```

**The sinks that need a third-party SDK are modules of their own** --
`sink/s3`, `sink/gcs`, `sink/azure`, `sink/sftp` -- so importing `spool` does
not put three cloud SDKs in your `go.sum`. Measured on a consumer importing
`spool` and `sink/dir`: 47 lines, none of them AWS, against 85 with 38 of them
AWS before the split. See [docs/EXTENDING.md](docs/EXTENDING.md).

## Building it

```sh
$ ./scripts/test.sh              # gofmt, vet, tests, and the coverage floor
$ docker buildx bake build test  # what CI runs; binaries land in ./dist
$ docker buildx bake app --load
$ docker run --rm ghcr.io/heojeongbo/wick:local version
```

Told nothing it runs everything and holds the floor, which is what a change is
finished against. While making one, three things narrow it:

```sh
$ MODULE=./sink/s3 ./scripts/test.sh          # one module, floor still held
$ PKG=./spool/... ./scripts/test.sh -run TestSettle
$ COVER=off PKG=./naming/... ./scripts/test.sh
```

`PKG` reports the coverage without holding it — a run that left most of the
tests out has nothing to say about whether everything is covered. CI sets none
of them.

There is a hundred per cent statement coverage gate, and the reason is in
`scripts/test.sh`: this is a thing that deletes files, and every branch nobody
has run is a branch that will first run on a machine holding the only copy of
something.

## Trying it

```sh
$ cd examples/local && ./up.sh
```

An object store, a WebDAV server, something writing a file every five seconds,
and wick carrying each one to three places at once — on your machine, with no
account anywhere. [examples/](examples/) has the rest: configurations for the
arrangements people actually run, and systemd units for putting it on a
machine.

## Documents

| | |
| --- | --- |
| [docs/SINKS.md](docs/SINKS.md) | every destination, every setting, and **what each one's read-back actually checks** |
| [docs/DESIGN.md](docs/DESIGN.md) | the ordering everything rests on, and what is true after a crash at each point |
| [docs/EXTENDING.md](docs/EXTENDING.md) | adding a kind, and releasing one |
| [examples/](examples/) | a demo that runs locally, configurations that are tested, and systemd units |
| [CHANGELOG.md](CHANGELOG.md), [SECURITY.md](SECURITY.md) | |

## What it does not do

It does not schedule itself against a calendar, it does not compress, it does
not encrypt, and it does not know what is in the files. Each of those is
somebody else's job and is easier to do well outside this than inside it.
