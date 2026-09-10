# Changelog

Versions are `vMAJOR.MINOR.PATCH`, and every module in this repository is
tagged with the same one. The container image is tagged `YYMMDD-r<n>` instead,
which is the build and not the release.

## Unreleased

A pass over the surfaces: what an operator reads when they type `--help`, what a
consumer reads on pkg.go.dev, and what a contributor sees when the gate fails.

**Fixed**

- **`Run` never asked the trigger.** `Trigger.Fire` had no caller anywhere. The
  loop woke on a timer and carried, so every trigger collapsed into the same
  one-minute poll: `every: 15m` carried every minute, `count: 10` carried at
  one, and a spool with no trigger — documented in three places as "only when
  asked" — carried constantly. **A deployment that relied on `every:` to space
  out its uploads has been uploading sixty times more often than it asked to.**
  The first pass is still not put to the trigger, so a daemon coming up on a
  week of recordings still hands them over at once.
- `spool` measured "how long since the last carry" from the last *successful*
  one, and pinned it to zero until there was one. With the fix above that would
  have meant a spool whose link was down never firing a clock trigger again. It
  measures from when the spool was made until there is a carry to measure from.
- The `sftp` sink answered a connection failure with an error that satisfied
  `errors.Is(err, fs.ErrNotExist)`, because dialling reads the key and the
  `known_hosts` and either being absent is an `*fs.PathError`. That is the one
  answer a sink gives which the engine acts on — it reads it as a write that did
  not stick — so a missing `known_hosts` meant re-sending the file every pass
  until it was set aside.
- `--config` after the subcommand was "unknown flag". `wick once --config x.yaml`
  works.
- A bad `journal.path` surfaced as bbolt's own message with nothing saying which
  path it was about.
- `go generate ./... && ./scripts/test.sh` failed on the file the first command
  had just written: the generated version file was not gofmt-clean.
- `.gitignore` had `/cover.out`, anchored to the root, so four of the five
  profiles the gate writes showed up as untracked after every run.
- `wick.yaml` was not in the CI path filter, so the test written to guard it
  never ran on a commit that changed it.

**Changed**

- **The one-shot commands are quiet.** `config`, `status`, `once` and `forget`
  wrote a daemon log line to stderr before doing anything; `-v`/`--verbose` puts
  it back. `run` is unchanged.
- **`wick config` does not print secrets.** They read `(set)`; `--reveal` gives
  back the form that loads again. A sink declares its own with a
  `wick:"secret"` tag, so one written elsewhere is covered without this
  repository having heard of it.
- Every command has a description saying what it is for, every flag has a
  letter, and a misuse prints the way out: `wick statuss` suggests `status`, and
  a missing argument prints that command's help.
- The gate prints 1.7 KB where it printed 23.5 KB, and takes `MODULE`, `PKG`
  and `COVER=off` so that it can be run while you work rather than only at the
  end.

**Added**

- `wick kinds` — what this build can carry from and to. The registries have
  always known and there was no way to ask.
- `wick check` — opens everything the configuration describes and then asks each
  sink whether it holds a name nothing is called. Carries nothing, deletes
  nothing, and leaves no journal behind if there was none.
- `wick completion zsh`.
- `wick status --json`.
- Runnable examples for `spool`, `trigger`, `retain`, `naming`, `sink`, `size`
  and `sinktest`, where there were none in any package.
- `source.Rooted`, which names the interface that decides whether
  `trigger.FreeBelow` and `retain.WhenFreeBelow` can work at all. It was an
  undeclared type assertion in `spool`.
- `size.Parse`, `config.Wick.Sinks` and `config.Wick.Reach`.
- A gate check that every list of modules in the repository agrees with
  `go.work` — the two that used to fail silently are the two that caused v0.2.1.

## v0.2.1

**Fixed**

- `sink/gcs`, `sink/azure` and `sink/sftp` at v0.2.0 cannot be built on their
  own. Each compiled against requirements it had never written down, because a
  sibling module in the workspace happened to have them; outside the workspace
  that is a missing `go.sum` entry. **Use v0.2.1.** The v0.2.0 tags stay where
  they are — the checksum database has recorded them, and a moved tag is a
  mismatch that cannot be undone.

## v0.2.0

Seven places to carry to, and the SDK-bearing ones in modules of their own.

**Added**

- `sink/gcs` — Google Cloud Storage. Service-account key or workload identity.
  The SHA-256 travels in the object's metadata, so the read-back is a hash.
- `sink/azure` — Azure Blob Storage. Connection string, account key, shared
  access signature or managed identity. The hash travels in the blob's metadata.
- `sink/sftp` — a server over SSH. Key or password, `known_hosts` required,
  written beside the name and renamed.
- `sink/webdav` — Nextcloud, ownCloud, a NAS front end. No new dependency.
- `sink/http` — Basic authentication, and a bearer token re-read from a file at
  every request.
- `sink/s3` — a profile, a role to assume with an external id and a session
  name, and a web identity token.
- `docs/` — `SINKS.md`, `DESIGN.md`, `EXTENDING.md`. `SECURITY.md`.

**Changed**

- `sink/s3` moved into a module of its own, joined by `gcs`, `azure` and `sftp`.
  A consumer importing `spool` and `sink/dir` now has a `go.sum` of 47 lines
  with no AWS in it, against 85 with 38 of them AWS.
- A configuration is read strictly: a key nothing answers to is refused rather
  than ignored. `settle_for` written under `source` instead of `carry` used to
  do nothing, silently.
- The coverage gate walks a module list; each module has its own hundred per
  cent.

**Fixed**

- The settle gate only knew what the running process had watched, so `wick once`
  — a fresh process every time a scheduler runs it — could never carry anything
  when `settle_for` was set. The file's own time decides now, with what the
  process watched as the fallback for a machine whose clock is behind every file
  it holds.
- The gate's exclusion pattern was passed to awk with `-v`, which reads escapes
  out of it: `\.g\.go` became `.g.go`, which matches `config.go`. Two files were
  being left out of the count.

## v0.1.0

The first one that carries anything.

- `source/dir` with a settle gate and an fsnotify overlay; `source/mem`.
- `sink/dir`, `sink/http`, `sink/s3`, `sink/mem`.
- `journal` on bbolt, `trigger`, `retain`, `naming`, `disk`, `size`.
- `spool` — the engine, and `spool.Group`.
- `wick run`, `once`, `status`, `forget`, `config`, `version`.
- Liveness and readiness split; OpenTelemetry counters and gauges.
