# Why it is built this way

## The rule everything follows from

> A false "not carried" costs one upload. A false "carried" loses the file.

There is no arrangement of those two mistakes in which the second is
acceptable, so every ordering decision resolves toward the first. Most of what
follows is that sentence applied to a particular question.

## The order of one carry

```
read → write to every sink → read each one back → record → retire
```

The journal is written at exactly two points, and the order is the whole design:

1. **after** every destination has been read back and confirmed → `Carried`
2. **after** the retention policy has acted → `Retired`

Nothing is written before the upload. There is no record that could claim a
success which did not happen.

### What is true after a crash at each point

| crash at | on disk | at the far end | restart does |
| --- | --- | --- | --- |
| mid-scan | untouched | untouched | rescans |
| mid-upload | untouched | partial or absent | re-uploads; same content, same name, overwritten |
| after upload, before verify | untouched | complete | re-verifies; `Stat` matches, records `Carried`, sends no bytes |
| after verify, before journal | untouched | complete | as above — one wasted `Stat`, no wasted bytes |
| after `Carried`, before retention | present | complete | retention runs from the `Carried` record |
| mid-`Move` | rename is atomic within a filesystem | complete | either name is found; the journal key is the source key, so it is not rediscovered |
| mid-`Delete` | unlink is atomic | complete | already gone, or deleted next pass |

Every row costs bytes at worst. No row loses a file.

### Every destination has to answer

A spool with two sinks records `Carried` only when both have confirmed, and the
journal remembers **which** confirmed. A restart in the middle of a fan-out does
not send again to the one that already had it — on a metered link that is the
difference between a retry and a bill.

## Two failures that look identical and mean opposite things

A sink saying *"I do not hold that"* and a source saying *"that is gone"* are
both `fs.ErrNotExist`.

- The source's means there is nothing to carry: forget the record.
- The sink's means a write did not stick: on no account let go of the file.

Reading the first as the second made a store that swallows writes look exactly
like a tidy-up. The source's is wrapped in a sentinel (`errGone`) at the one
place it can arise, and the sink's is never given that treatment. This was found
by a test, not by reading.

## The settle gate, and why it is in the engine

A file that is still being written looks exactly like a file that has been
written. The only way to tell is to look twice.

It is in the engine rather than in each source because looking twice is
something the engine does anyway — it scans on a schedule — and putting it in
the source would mean every source implementing the same code.

It has two tests, and the order matters:

1. **the file's own time.** `now - mtime >= settle`. This is the one that
   survives a restart, and it has to come first: `wick once` is a fresh process
   every time a scheduler runs it, so a gate that only knew what *this* process
   had watched would say "not yet" on every single run — a one-shot that can
   never carry anything.
2. **what this process has watched.** Only reached when the file claims to have
   been written in the future, which is what a machine that boots without a
   network and has its clock put right hours later sees. What was watched
   happen is still true then.

This is also why the filesystem watcher is allowed to be wrong. It only ever
says "look again"; the scan decides. An event that is lost, doubled, or about
something that no longer exists costs nothing.

## What is refused, and where

Two kinds of question, asked in two places.

**About the configuration as a document** — `Config.Evaluate`. A spool with no
name, two spools with one name, a destination naming a sink nothing declares, a
`move` with nowhere to move to, a duration below zero. These are true or false
by reading, and are refused before anything is opened. `wick config` therefore
works on a machine that could never run.

**About the machine** — `Config.Build`. A journal that cannot be made, a bucket
that cannot be reached, a retention that deletes from a source that cannot
delete. These are asked at startup, all of them, before anything is carried.

A key that is not base64, a certificate without its key, a `known_hosts` that is
not there: each is refused when the sink is built, not at the first carry at
three in the morning.

## Reading is strict

A key nothing answers to is refused, not ignored. It is the same rule the
environment reader already had — a `WICK_*` variable that answers to nothing is
reported — and it exists because `settle_for` written under `source` instead of
`carry` did nothing, silently, and that is an afternoon.

## What is set aside rather than retried forever

After `attempts` failures an item is `Quarantined` with the reason, and stops
being tried. One thing that cannot be carried must not spend the whole link on
itself while everything behind it waits. `wick status --quarantined` lists them;
`wick forget` releases one. An item that becomes a *different* file — a new size
or time — is no longer the one that was set aside, and is carried.

## What a template must not guess

Retention has no default. `keep` is what a configuration that said nothing gets,
and that is deliberate: there is no sensible default for deleting a file,
because the sensible default depends on what the file is. A template that
guesses is a template that deletes something it should not have on a machine
nobody was watching.

The same rule elsewhere: a trigger with nothing set never fires; a naming
template that says nothing means the name the file already had; a filesystem
that cannot be measured reads as "nobody could say" and never as "no room".

## The one metric that matters

`wick.carry.last_success` — seconds since something was last carried.

Everything else can be worked out from a log, and none of it makes the failure
this daemon actually has visible: uploads stop, the disk keeps filling, and
every other signal reads exactly as it does on a machine that has nothing to
carry.

Liveness and readiness are separated for a related reason. `/healthz` says yes
while the link is down, because the link being down is the case this exists for
— a liveness probe that failed on it would restart every machine in the fleet at
the same moment, during the outage. `/readyz` fails only when the *journal* is
unreachable, because carrying without knowing what has already been carried is
how a file gets deleted twice.

## The layout, and the module cycle

The sinks that need a third-party SDK are modules of their own, so that
importing `spool` does not put the AWS, Google and Azure SDKs into a consumer's
`go.sum`. Measured on a consumer importing `spool` and `sink/dir`: 85 lines with
38 of them AWS, against 47 lines with none.

The binary imports the sinks and the sinks import the root, which is a cycle
between modules. Go allows it, and pseudo-versions cannot close it — a
pseudo-version sorts as a pre-release, so a literal `v0.0.0` outranks every one
of them. Tags can, and that is what releasing is: a tag per module, all naming
the same version. See [EXTENDING.md](EXTENDING.md).

A committed `go.work` is what stops a checkout ever caring.

## Why there is a hundred per cent coverage gate

This is a thing that deletes files. Every branch it takes that nobody has run is
a branch that will first run on a machine holding the only copy of something.

It is not a claim that the tests are good. It is a claim that there is no code
here nobody has ever executed, which is a much smaller claim and the one worth
keeping. It has already caught itself: the exclusion pattern was passed to awk
with `-v`, awk read the escapes out of it, and `\.g\.go` became `.g.go` — which
matches `config.go`. Two files were being left out of the count, silently, by
the thing whose whole job is to notice that.
