package cmd

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/z"

	"github.com/heojeongbo/wick/journal"
)

// The two probes answer two different questions, and the difference matters
// more here than it looks.
//
// Liveness is "is this process worth keeping". It says yes as long as the
// process is running, and in particular it says yes while the link is down --
// because the link being down is the case this daemon exists for, and a
// liveness probe that failed on it would restart every machine in the fleet at
// the same moment, during the outage, losing whatever was half-carried.
//
// Readiness is "can this do its job". The only thing it cannot do its job
// without is the journal: without that it cannot know what it has already
// carried, and carrying without knowing is how a file gets deleted twice.
const (
	pathLiveness  = "/healthz"
	pathReadiness = "/readyz"
)

const (
	// storeCheckTimeout is how long the journal is given to answer. Longer
	// than any answer it has ever taken, short enough that a probe does not
	// hang on it.
	storeCheckTimeout = 3 * time.Second

	// headerTimeout is here because without it a connection that says nothing
	// holds a goroutine for as long as it likes.
	headerTimeout = 5 * time.Second

	// shutdownGrace is how long an answer in flight is given to finish.
	shutdownGrace = 3 * time.Second
)

// serveHealth answers the probes until ctx is done.
//
// An endpoint that was not asked for is not served, and that is not an error:
// a machine running this under something that does not probe has nothing to
// gain from a port being open.
func serveHealth(ctx context.Context, endpoint string, jnl journal.Journal) (string, func(), error) {
	if endpoint == "" {
		return "", func() {}, nil
	}

	l := log.From(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc(pathLiveness, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(pathReadiness, func(w http.ResponseWriter, r *http.Request) {
		ctx, done := context.WithTimeout(r.Context(), storeCheckTimeout)
		defer done()

		if err := ping(ctx, jnl); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)

			return
		}

		w.WriteHeader(http.StatusOK)
	})

	ln, err := listen("tcp", endpoint)
	if err != nil {
		return "", nil, z.Err(err, "listen for probes on %q", endpoint)
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: headerTimeout}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			l.Warn("the probes are not being answered", slog.String("error", err.Error()))
		}
	}()

	// The address that was actually taken, not the one that was asked for: a
	// port of zero is a real thing to write in a configuration and a useless
	// thing to read in a log.
	addr := ln.Addr().String()
	l.Info("answering probes", slog.String("endpoint", addr))

	return addr, func() {
		ctx, done := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer done()

		_ = srv.Shutdown(ctx)
	}, nil
}

// listen is a variable for the same reason [journal.Journal] is handed over: a
// listener that stops listening is a thing that happens to a machine, and there
// is no way to make a real one do it on demand.
var listen = net.Listen

// ping asks the journal whether it is answering. What is asked about does not
// matter; that it answered does.
func ping(ctx context.Context, jnl journal.Journal) error {
	_, _, err := jnl.Get(ctx, "", "")

	return z.ErrIf(err, "the journal is not answering")
}
