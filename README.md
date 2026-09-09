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
| **Somewhere to carry to** | S3-compatible storage, an HTTP server the fleet already runs, or another directory. Several at once |
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

**A file is not eligible until it has stopped changing.** A file that is still
being written looks exactly like a file that has been written. `settle_for` is
the only thing that tells them apart, and it is why the filesystem watcher is
allowed to be wrong: it only ever says "look again", and the scan is what is
true.

## Getting started

```sh
$ wick config          # what it thinks it has been told
$ wick once            # carry what is due, once
$ wick status          # what the journal knows
$ wick run             # watch, and carry what turns up
```

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
| `source` | where things accumulate. Three methods, plus `Remover`, `Mover` and `Watcher` for what an implementation can also do |
| `sink` | where they go. One method, plus `Stater` for reading one back |
| `journal` | what has already gone |
| `trigger` | when to go: `Every`, `Count`, `Bytes`, `FreeBelow`, `Any`, `All` |
| `retain` | what becomes of the original: `Keep`, `Delete`, `Grace`, `Move`, `WhenFreeBelow`, `First` |
| `naming` | what a thing is called once it is somewhere else |
| `spool` | the engine |

Adding a kind of sink is implementing two methods and calling `sink.Register`
from your package's `init`. Importing your package is then what makes
`type: yours` mean something -- see `cmd/kinds.go`, which is the whole of what
this build can do. There are conformance suites for both halves, so an
implementation can be held to the contract rather than to the doc comments:

```go
func TestMySink(t *testing.T) {
	sinktest.Suite(t, func(t *testing.T) sink.Sink { return New(...) })
}
```

## Building it

```sh
$ ./scripts/test.sh              # gofmt, vet, tests, and the coverage floor
$ docker buildx bake build test  # what CI runs; binaries land in ./dist
$ docker buildx bake app --load
$ docker run --rm ghcr.io/heojeongbo/wick:local version
```

There is a hundred per cent statement coverage gate, and the reason is in
`scripts/test.sh`: this is a thing that deletes files, and every branch nobody
has run is a branch that will first run on a machine holding the only copy of
something.

## What it does not do

It does not schedule itself against a calendar, it does not compress, it does
not encrypt, and it does not know what is in the files. Each of those is
somebody else's job and is easier to do well outside this than inside it.
