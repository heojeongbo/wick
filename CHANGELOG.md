# Changelog

Versions are `vMAJOR.MINOR.PATCH`, and every module in this repository is
tagged with the same one. The container image is tagged `YYMMDD-r<n>` instead,
which is the build and not the release.

## Unreleased

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
