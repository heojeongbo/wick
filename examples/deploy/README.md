# Putting it on a machine

Two ways, and they are not the same decision.

**`wick.service`** runs the daemon. Right when files appear at any hour and the
trigger should decide when they go — which is most robots.

**`wick-once.service` + `wick-once.timer`** runs one pass on a schedule. Right
when the machine is only awake sometimes, or when something else already owns
when things happen on it.

`once` does not consult the trigger: whoever ran it has already decided that now
is the time. So the `trigger:` block does nothing under the timer, and the timer
*is* the schedule. Under the daemon it is the other way round.

All three units verify clean under `systemd-analyze verify`.

## Before enabling anything

```sh
# The account it runs as. It needs to read the recordings, write its journal,
# and reach the network. Nothing else.
sudo useradd --system --no-create-home --shell /usr/sbin/nologin wick

sudo install -d -m 0750 -o wick -g wick /etc/wick
sudo install -m 0640 -o root -g wick wick.yaml /etc/wick/wick.yaml

# Credentials, if this machine has to hold any. Root-owned, group-readable by
# wick, and never in the configuration file.
sudo install -m 0640 -o root -g wick /dev/null /etc/wick/env
sudo tee /etc/wick/env >/dev/null <<'ENV'
S3_KEY_ID=AKIA...
S3_KEY_SECRET=...
ENV

sudo install -m 0644 wick.service /etc/systemd/system/
sudo systemctl daemon-reload
```

Then, before starting it:

```sh
sudo -u wick wick check --config /etc/wick/wick.yaml
```

That opens everything the configuration describes and asks each destination
whether it is there. Run as `wick`, because the answer that matters is whether
*that* account can read the recordings and reach the bucket — not whether you
can.

## The things worth getting right

**Credentials by environment.** `EnvironmentFile=-/etc/wick/env`, chmod 0640,
root-owned. A key in `wick.yaml` is a key in whatever backup takes that file and
in whatever paste somebody makes of it. `${env:S3_KEY_SECRET}` in the file names
it instead; `wick config` prints it as `(set)`.

Better still, where the platform offers it: no credentials at all. An instance
role, a workload identity, a managed identity. All three are minted by something
else, rotated by something else, and can be taken away without touching the
machine.

**`ReadWritePaths` has to include the source.** `ProtectSystem=strict` makes the
whole filesystem read-only, and a retention that deletes or moves has to be able
to write to the directory it is deleting from. If it is not listed, everything
carries and nothing is ever tidied — which looks like it is working, until the
disk fills.

**`TimeoutStopSec` is how long a carry gets to finish.** systemd sends SIGTERM,
wick takes it as "finish what you are doing", and then systemd sends SIGKILL
when the timeout runs out. Five minutes is generous for one file on a slow link.
Nothing is lost either way — a carry that is cut off has written no record, so
the next pass sends it again — but a shutdown that always kills is a shutdown
that always wastes the bytes in flight.

**Jitter, on a fleet.** `RandomizedDelaySec=60` in the timer. Without it every
machine carries at the same second and the shared uplink becomes a queue.

## Watching it

```sh
journalctl -u wick -f
sudo -u wick wick status --config /etc/wick/wick.yaml
```

`wick status` wants the journal, and the daemon has it open. Either stop the
daemon first or expect:

```
error: the journal "/var/lib/wick/wick.db" is held by something else, most
likely a `wick run` already going: only one thing at a time may have it open
```

`--json` is there for something that is not a person.

## The one alert worth having

Set `health.endpoint` and scrape `wick.carry.last_success` — seconds since
something was last carried. It is the only signal that distinguishes the failure
this daemon actually has, which is that uploads stop while everything else looks
exactly as it does on a machine that simply has nothing to carry.

`/healthz` deliberately stays up while the link is down: the link being down is
the case this exists for, and a liveness probe that failed on it would restart
every machine in the fleet at once, during the outage. `/readyz` fails only when
the journal cannot be reached.
