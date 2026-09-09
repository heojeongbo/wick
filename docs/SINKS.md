# Places to carry to

Seven kinds. A spool carries to any number of them at once and is only done when
every one has confirmed, so "the cloud and the server in the building" is one
arrangement rather than two.

## What each one's read-back actually checks

This is the table to read first. Nothing else on this page matters as much.

| | confirms | because |
| --- | --- | --- |
| `s3` | **hash** | S3 takes a SHA-256 and gives it back |
| `gcs` | **hash** | the hash goes in the object's metadata |
| `azure` | **hash** | the hash goes in the blob's metadata |
| `dir` | length | a filesystem keeps no hash, and reading the file back to take one costs what writing it did |
| `http` | length, or hash if the server keeps one — see `digest_header` | |
| `webdav` | length | WebDAV keeps no hash |
| `sftp` | length | nor does SFTP |

A length catches a stream that ended early, which is the failure that actually
happens. It does not catch a file that arrived whole and wrong. Where the
distinction matters, prefer a store from the first three rows.

Every sink also checks the length before it finishes, so a spool told
`verify: false` still catches a short stream. That is the floor; the read-back
is the thing above it.

---

## `s3` — AWS, and everything that speaks its language

MinIO, Ceph, R2, Wasabi, versitygw, and S3 itself.

```yaml
sinks:
  cloud:
    type: s3
    bucket: recordings
    prefix: robots              # in front of every name
    endpoint: https://s3.example.com   # AWS itself when unset
    region: auto                # many compatible stores want one and ignore it
    path_style: false           # true for a store reached by address, not name

    # Who this is. One of the four, or none of them.
    access_key_id: "${env:S3_KEY_ID}"
    secret_access_key: "${env:S3_KEY_SECRET}"
    session_token: ""
    profile: robot              # out of the shared configuration
    # ...or nothing at all: an instance role, the environment, the metadata
    # service. That is the way round to prefer -- a long-lived key on a machine
    # in a cupboard is a long-lived key on a machine somebody can walk up to.

    # Who this then becomes.
    assume_role_arn: arn:aws:iam::123456789012:role/recordings
    role_session_name: thor-top      # the only thing in the other account's
                                     # logs that says which machine
    external_id: "${env:ROLE_EXTERNAL_ID}"
    web_identity_token_file: /var/run/secrets/token   # needs assume_role_arn

    part_size: 16MiB            # how a large object is broken up
    concurrency: 4              # parts in the air at once
    rate_limit: 20MiB           # bytes a second; no limit when unset

    ca_file: /etc/ssl/certs/internal.pem
    cert_file: /var/lib/wick/client.crt
    key_file: /var/lib/wick/client.key
    insecure: false
```

Refused before anything is sent: a key and a profile, a key and a token, or a
profile and a token — each pair is two answers to one question. A token with no
`assume_role_arn` to exchange it for. An `external_id` or a `role_session_name`
with no role.

The role is not taken on at startup. Building the provider is construction; the
exchange happens at the first request that needs signing, so a role that is
refused fails a carry and is retried rather than stopping the daemon.

## `gcs` — Google Cloud Storage

```yaml
sinks:
  cloud:
    type: gcs
    bucket: recordings
    prefix: robots
    credentials_file: /var/lib/wick/key.json
    credentials_json: "${env:GCP_KEY}"   # the same thing, held rather than named
    # ...or neither: workload identity on GKE, the metadata server on Compute
    # Engine. Short-lived, rotated by something else, not in a file.
    endpoint: ""                # Google's own when unset
    chunk_size: 16MiB           # buffered and sent at a time
    rate_limit: 20MiB
```

The SHA-256 is written to the object's metadata under `sha256` and read back
from there. A store that has lost it answers with the length alone, and the
engine checks that instead.

## `azure` — Azure Blob Storage

```yaml
sinks:
  cloud:
    type: azure
    account: wickstorage
    container: recordings
    prefix: robots
    service_url: ""             # worked out from the account when unset; set it
                                # for a private endpoint or an emulator

    # One of the three, or none of them.
    connection_string: "${env:AZURE_CONNECTION_STRING}"
    account_key: "${env:AZURE_ACCOUNT_KEY}"
    sas: "${env:AZURE_SAS}"
    # ...or nothing: a managed identity, which the platform mints, rotates and
    # can take away.

    block_size: 1MiB            # each concurrent upload holds a buffer of one
    concurrency: 1
    rate_limit: 20MiB
```

Azure canonicalizes metadata names — what is written as `sha256` comes back as
`Sha256` — so the names are folded on the way in. That is tested against the
real client rather than assumed.

## `http` — a server the fleet already runs

No SDK, no notion of a region. A base address, a method, and whatever the far
end wants to be told.

```yaml
sinks:
  onsite:
    type: http
    endpoint: https://collector.example.com/drop
    method: PUT                 # or POST
    digest_header: X-Content-Sha256   # sent, and read back out of; unset checks
                                      # the length alone

    # Who is asking. Headers plus one of the two.
    headers:
      X-Robot: thor-top
    username: wick
    password: "${env:COLLECTOR_PASSWORD}"
    token_file: /var/run/secrets/token   # read at every request

    rate_limit: 20MiB
    ca_file: /etc/ssl/certs/internal.pem
    cert_file: /var/lib/wick/client.crt   # read again at every handshake
    key_file: /var/lib/wick/client.key
    insecure: false
```

A username and a `token_file` together is refused: both set `Authorization` and
one would quietly win.

The token file and the client certificate are both re-read rather than held.
On these machines something else renews them, and holding the first one means
working until it expires and then failing until somebody restarts the daemon.

## `webdav` — Nextcloud, ownCloud, a NAS front end

The same settings as `http` minus `method` and `digest_header`, plus one
behaviour: a WebDAV server will not accept a PUT into a collection that is not
there, so the collections a name needs are made first — once each, and
remembered.

```yaml
sinks:
  nas:
    type: webdav
    endpoint: https://nas.example.com/remote.php/dav/files/wick
    username: wick
    password: "${env:NAS_PASSWORD}"
    rate_limit: 20MiB
```

## `sftp` — the one a building already has

```yaml
sinks:
  onsite:
    type: sftp
    address: files.example.com:22       # port 22 when unset
    user: wick
    path: /srv/recordings               # where the account lands when unset

    # One of the two.
    key_file: /var/lib/wick/id_ed25519
    key_passphrase: "${env:SSH_KEY_PASSPHRASE}"
    password: "${env:SFTP_PASSWORD}"

    known_hosts: /etc/ssh/ssh_known_hosts
    insecure_ignore_host_key: false

    timeout: 30s                # reaching the server, not carrying a file
    rate_limit: 20MiB
```

`known_hosts` is **required** unless `insecure_ignore_host_key` is set. A host
key nobody checks is a sink that will one day be somebody else, and these are
the files nobody is watching.

It writes to `.name.partial` and renames, so anything watching that directory
sees the whole file or no file. The connection is made once and kept: a spool
carrying a thousand files is otherwise a thousand handshakes.

## `dir` — a disk, a NAS, a drive somebody carries

```yaml
sinks:
  nearby:
    type: dir
    path: /mnt/collect
```

The write is atomic — a temporary name and then a rename — which is what makes
a two-step arrangement work: one wick carries into a directory another machine
can see, and a second wick carries from there onward.

---

## What this build can do

`type:` resolves through a registry each sink registers itself into from its
`init`, so **importing the package is what makes the kind available**.
`cmd/kinds.go` is the whole of that list. Removing a line there and running
`go mod tidy` takes the dependency out of the binary as well — which is the
point of the SDK-bearing sinks being modules of their own.

See [EXTENDING.md](EXTENDING.md) for adding one.
