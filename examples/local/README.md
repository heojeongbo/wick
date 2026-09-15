# The whole thing, on your machine, with no account anywhere

```sh
$ ./up.sh
```

That builds the image this repository makes and brings up five containers: an
object store, a WebDAV server, something writing a file every five seconds, and
wick carrying each of those files to three places at once.

Stop it with Ctrl-C. `docker compose down -v` takes the volumes with it.

## What you are looking at

```
wick-1 | watching - spool=recordings source=dir to=3 when=any of [every 30s, 2 or more waiting, less than 1073741824 bytes free] then=delete
wick-1 | carried - spool=recordings carried=1 bytes=786432 retired=1 set_aside=0
```

`bytes=786432` is 256 KiB sent three times, which is the point: one file, three
destinations, counted once per destination it went to. `retired=1` is the
original being deleted — and it is only deleted because all three confirmed it.

`carried=1` every five seconds, with `count: 2` in the configuration, is worth a
second look. Two files really are waiting each time the trigger is asked. One of
them was written less than two seconds ago and `settle_for: 2s` says it has not
stopped changing yet, so the pass carries the older one and leaves the younger.
The trigger decides *whether* a pass happens; the settle gate decides *what* is
in it.

## Things to try

**Where the files went.**

```sh
$ docker compose exec minio mc alias set d http://localhost:9000 demo demo-secret
$ docker compose exec minio mc ls --recursive d/recordings
[...] 256KiB STANDARD thor-top/2026/09/15/session-008.rec

$ docker compose exec webdav find /var/lib/dav/data -type f
/var/lib/dav/data/2026-09-15/session-006.rec
```

Three destinations, three different names for the same file. That is what
`name_as` is per-destination for: a bucket wants a prefix and a date, a NAS
wants a folder per day, the disk next door wants the name it already had.

**The secrets are not in the file.**

```sh
$ docker compose run --rm --no-deps wick config | grep -E 'secret_access_key|password'
    secret_access_key: (set)
    password: (set)
```

They are named in `wick.yaml` as `${env:...}` and held in the environment. Add
`--reveal` to get the form that can be loaded back, which is a thing you have to
type.

**What it would check before starting.**

```sh
$ docker compose run --rm --no-deps wick check
```

It opens everything and then asks each sink whether it holds a name nothing is
called. Try breaking something first — change the bucket in `wick.yaml` to one
that does not exist, and watch it be named.

**The probes.**

```sh
$ curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:9000/healthz
200
```

`/healthz` stays up while the link is down, because the link being down is the
case this exists for and a probe that failed on it would restart every machine
in a fleet at once. `/readyz` fails only when the journal cannot be reached.

**Asking about the journal while the daemon has it.**

```sh
$ docker compose run --rm --no-deps wick status
error: the journal "/var/lib/wick/wick.db" is held by something else, most likely a `wick run` already going: only one thing at a time may have it open
```

That refusal is deliberate. Two processes carrying and deleting the same files
is worse than one of them not starting. `docker compose stop wick` first, then
ask.

## What is not here

There is no `sftp` destination in this stack, and that is a decision about the
demo rather than about wick: the SSH server images it would rest on are the
least predictable part of it, and a demo that does not come up the first time is
worth nothing.

[`../robot-to-cloud-and-onsite.yaml`](../robot-to-cloud-and-onsite.yaml) is how
one is written. `wick check` is how it is tested without waiting for a carry: it
reads the `known_hosts` and reaches the server, which is the part somebody gets
wrong.

There is also no `gcs` or `azure` here, for the plain reason that a convincing
fake for either is more machinery than this page is worth. Both are configured
the same shape; see [`../../docs/SINKS.md`](../../docs/SINKS.md).
