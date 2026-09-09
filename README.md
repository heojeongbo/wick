# wick

My flavor of "Hello, World!" for Go app.

## Which branch you want

**There is no server on this one.** `main` is the base every kind is made from:
configuration, logging, telemetry, the Docker build, and a `greet` command to
prove it runs. Take it as it is if what you are making is a command.

A service is a branch:

| | |
| --- | --- |
| [`kind/server`](../../tree/kind/server) | a gRPC service. Entities declared once as protobuf and generated into messages, an ent schema, migrations and CRUD servers; a layered server stack; authentication and a gate in front of it; `List`, `Watch`, rate limiting, grpc-web, and a React page on the other end of the same protos. |
| [`kind/server-x`](../../tree/kind/server-x) | the same, with a wall: a Tenant every row belongs to, a predicate in every query saying which of them a caller may read, and an audit trail. |

Read the README of the branch itself; each says what it is and what to do next.

### Taking one

**"Use this template" copies the default branch and no other**, and the default
branch is this one. So tick **"Include all branches"** when GitHub asks, or the
new repository will have nothing but what you are reading.

Then, in the copy it made for you, make the kind you want into the branch you
work on — an app has one, and it is `main`:

```sh
$ git clone https://github.com/your-name/my-app.git && cd my-app
$ git reset --hard origin/kind/server     # whichever kind you took
$ git push --force origin main

# The other kinds are somebody else's demonstration now.
$ git push origin --delete kind/server kind/server-x
```

Without the button, the same thing is one clone:

```sh
$ git clone --branch kind/server --single-branch --depth 1 \
    https://github.com/heojeongbo/wick.git my-app
$ cd my-app && rm -rf .git && git init && git add -A && git commit -m "from wick"
```

Either way `go run ./tools/init` is the next thing you run, and it is the same
command on every branch. `--help` lists what that branch can leave out; this one
is the base and can leave out nothing.

*Then delete this section: it is about taking the template, and you have.*

## Quick Start

```sh
# Make it yours: the module path, the binary, the config file and the `WICK_`
# environment prefix. It builds what it changed, and then removes itself.
$ go run ./tools/init github.com/your-name/your-app

# Build and test the app.
# Build results will be placed in the `/dist` directory.
$ docker buildx bake build test

# Load apps into the local Docker engine.
$ docker buildx bake app --load
$ docker run --rm ghcr.io/heojeongbo/wick:local greet
> |........| 19:08:03.037 ○ 000000 000000 use default config
> Hello, hypnos!
```

## Configuration

The app reads `wick.yaml`, or the file `--config` names. What it says can be
overruled, and the last word wins:

```
built-in defaults  <  the file  <  the environment  <  the flags
```

### From the environment

Every field answers to a variable named after the path to it: `format` of
`greet` is `WICK_GREET_FORMAT`, and so on for however deep the field sits.
`config` prints what the app ended up with, which is how to check.

```sh
$ WICK_GREET_FORMAT='Hi, %s.' go run . greet hypnos
> Hi, hypnos.
```

A name that starts with `WICK_` and answers to nothing is reported rather
than ignored in silence, since that is what a typo looks like:

```
$ WICK_GREET_FORMATT=x go run . greet hypnos
> ! no configuration is read from these, so they were ignored - env=[WICK_GREET_FORMATT]
```

The names are made from the configuration itself, so a field that is added
answers to one without anything being written here or anywhere else.

### From the environment, inside the file

A value in the file can name a variable instead of holding one, the way the
OpenTelemetry Collector spells it. This is for the parts of a value that must
not be written down, such as the password inside a connection string.

```yaml
greet:
  format: "${env:GREETING:-Hello}, %s!"
```

Without `:-default` the variable is required: one that is not set is an error
rather than an empty string, so a missing secret is noticed at startup instead
of at the first request. Write `$$` for a literal dollar sign.
