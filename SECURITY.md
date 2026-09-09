# Security

## Reporting

Do not open an issue. Use GitHub's private security advisory form on this
repository, under **Security → Report a vulnerability**.

Say what an attacker can do and what they need in order to do it. A proof is
welcome and a working exploit is not required.

## What this holds and what it can reach

`wick` runs where the files are. It is worth knowing what that gives it:

- **Read of everything under a watched directory**, and, with a retention that
  deletes or moves, the ability to take those files away.
- **The credentials of every sink**, held for the life of the process. A key
  written in the configuration file is a key on that machine's disk.
- **The journal**, which is a record of what was carried and where. It holds
  names and hashes, not contents.

It does not read what it carries beyond hashing it, does not listen on a port
unless `health.endpoint` is set, and does not phone anywhere it was not pointed
at.

## What the design already refuses

- A `known_hosts` is required for SFTP; turning it off is a field with
  `insecure` in its name.
- A name that would climb out of the place it was meant for is refused, for
  every sink whose names are paths.
- Two ways of saying who a caller is are refused together rather than one
  quietly winning.
- A secret can be named rather than held: `"${env:NAME}"` in the configuration
  is read from the environment, and one that is neither set nor given a default
  is an error at startup rather than an empty string.

## What it does not do

It does not encrypt what it carries. If the bytes must not be readable at the
far end, encrypt them before they are written where `wick` can see them — that
is a smaller and better-understood job than one this would do on the way past.

It does not manage credentials. Where a platform will mint short-lived ones — an
instance role, workload identity, a managed identity — prefer that over a key in
a file, and say so by leaving the key out of the configuration.
