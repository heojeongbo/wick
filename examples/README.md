# Examples

## Start here

**[`local/`](local/) — the whole thing running on your machine.** One command,
no account anywhere: an object store, a WebDAV server, something writing files,
and wick carrying each of them to three places at once. It is the only way to
watch the fan-out, the read-back and the retention actually happen.

```sh
$ cd local && ./up.sh
```

## Configurations

Every one of these is loaded and evaluated by the test suite, so what is here
parses and means something. Copy the closest one and change it.

| | |
| --- | --- |
| [`robot-to-s3.yaml`](robot-to-s3.yaml) | the plainest arrangement: one directory, one bucket, delete a day after it arrives |
| [`robot-to-cloud-and-onsite.yaml`](robot-to-cloud-and-onsite.yaml) | the cloud and the server in the building at once — **one spool with two destinations**, which is what makes "only delete when both have it" true |
| [`two-step-edge.yaml`](two-step-edge.yaml) | a robot with no uplink, carrying to a share |
| [`two-step-uplink.yaml`](two-step-uplink.yaml) | the machine that does have one, taking it from there to the cloud |
| [`when-the-disk-fills.yaml`](when-the-disk-fills.yaml) | carry nothing until there is no room, then carry |

`local/wick.yaml` is a sixth, and is the one that has actually been run.

## Putting it on a machine

**[`deploy/`](deploy/)** — systemd units for both ways of running it, and the
things worth getting right: credentials by environment rather than in the file,
`ReadWritePaths` covering the source so that retention can actually delete,
`TimeoutStopSec` long enough for a carry to finish, and jitter so that a fleet
does not carry at the same second.

All three units verify clean under `systemd-analyze verify`.

## What is not here

No `gcs` or `azure` example of their own: both are the same shape as `s3` with a
different block under `sinks:`, and [`docs/SINKS.md`](../docs/SINKS.md) has
every setting for all seven kinds with what each one's read-back actually
checks.

No `sftp` in the local demo — three SSH server images refused password
authentication in three different ways, and a demo that does not come up the
first time is worth nothing. `robot-to-cloud-and-onsite.yaml` is how one is
written, and `wick check` reads the `known_hosts` and reaches the server, which
is the part people get wrong.

## Before you trust any of them

```sh
$ wick kinds                  # what this build can carry from, and to
$ wick config --config x.yaml # what it understood, secrets shown as (set)
$ wick check  --config x.yaml # whether that would work on this machine
```

None of those three move a file.
