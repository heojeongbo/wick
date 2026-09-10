package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lesomnus/otx"
	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/journal"
	"github.com/heojeongbo/wick/journal/bolt"
	journalmem "github.com/heojeongbo/wick/journal/mem"
)

var errRefused = errors.New("refused")

func newJournal(t *testing.T) journal.Journal {
	t.Helper()

	j, err := bolt.Open(filepath.Join(t.TempDir(), "wick.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = j.Close() })

	return j
}

// The first signal means "finish what you are doing"; the second means "now".
func TestStopping(t *testing.T) {
	t.Run("it stops when the carrying stops on its own", func(t *testing.T) {
		x := require.New(t)

		x.NoError(run(t.Context(), func(context.Context) error { return nil }))
		x.ErrorIs(run(t.Context(), func(context.Context) error { return errRefused }), errRefused)
	})

	t.Run("it stops when the running stops", func(t *testing.T) {
		x := require.New(t)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		x.NoError(run(ctx, func(ctx context.Context) error {
			<-ctx.Done()

			return nil
		}))
	})

	t.Run("the first signal lets what is in flight finish", func(t *testing.T) {
		x := require.New(t)

		carrying := make(chan struct{})
		done := make(chan error, 1)

		go func() {
			done <- run(t.Context(), func(ctx context.Context) error {
				close(carrying)
				<-ctx.Done()
				// The half-second is the thing being tested: it is given time
				// to finish rather than cut off.
				time.Sleep(10 * time.Millisecond)

				return nil
			})
		}()

		<-carrying
		x.NoError(syscall.Kill(os.Getpid(), syscall.SIGTERM))
		x.NoError(<-done)
	})

	t.Run("the second stops it where it is", func(t *testing.T) {
		x := require.New(t)

		carrying := make(chan struct{})
		done := make(chan error, 1)

		go func() {
			done <- run(t.Context(), func(ctx context.Context) error {
				close(carrying)
				// Never finishes, which is what a carry that has hung looks
				// like -- and what the second signal is for.
				select {}
			})
		}()

		<-carrying
		x.NoError(syscall.Kill(os.Getpid(), syscall.SIGTERM))
		// The first has to be taken before the second is sent, or the second
		// is the one that is seen first.
		time.Sleep(20 * time.Millisecond)
		x.NoError(syscall.Kill(os.Getpid(), syscall.SIGTERM))

		x.NoError(<-done)
	})
}

// Liveness says yes while the link is down, because the link being down is the
// case this daemon exists for. Readiness is the one that fails on a journal
// that has stopped answering.
func TestTheProbes(t *testing.T) {
	t.Run("one that was not asked for is not served, and that is not a failure", func(t *testing.T) {
		x := require.New(t)

		addr, stop, err := serveHealth(t.Context(), "", journalmem.New())
		x.NoError(err)
		x.Empty(addr)
		x.NotNil(stop)
		stop()
	})

	t.Run("an address that cannot be listened on is said so", func(t *testing.T) {
		x := require.New(t)

		_, _, err := serveHealth(t.Context(), "256.256.256.256:1", journalmem.New())
		x.ErrorContains(err, "listen for probes")
	})

	t.Run("both of them answer while the journal does", func(t *testing.T) {
		x := require.New(t)

		jnl := newJournal(t)
		addr, stop, err := serveHealth(t.Context(), "127.0.0.1:0", jnl)
		x.NoError(err)
		defer stop()

		x.Equal(http.StatusOK, get(t, "http://"+addr+pathLiveness))
		x.Equal(http.StatusOK, get(t, "http://"+addr+pathReadiness))
	})

	t.Run("readiness stops when the journal does, and liveness does not", func(t *testing.T) {
		x := require.New(t)

		jnl := newJournal(t)
		addr, stop, err := serveHealth(t.Context(), "127.0.0.1:0", jnl)
		x.NoError(err)
		defer stop()

		x.NoError(jnl.Close())

		// A machine whose journal has gone should be taken out of whatever is
		// asking. It should not be restarted, which is what liveness failing
		// would do to every one of them at once.
		x.Equal(http.StatusServiceUnavailable, get(t, "http://"+addr+pathReadiness))
		x.Equal(http.StatusOK, get(t, "http://"+addr+pathLiveness))
	})

	t.Run("asking the journal is asking whether it answers", func(t *testing.T) {
		x := require.New(t)

		x.NoError(ping(t.Context(), journalmem.New()))

		jnl := newJournal(t)
		x.NoError(jnl.Close())
		x.ErrorContains(ping(t.Context(), jnl), "not answering")
	})
}

func get(t *testing.T, url string) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)

	return res.StatusCode
}

// A number of bytes is said the way somebody reads them.
func TestSayingHowMany(t *testing.T) {
	x := require.New(t)

	x.Equal("0 B", bytes(0))
	x.Equal("512 B", bytes(512))
	x.Equal("1.0 KiB", bytes(1024))
	x.Equal("1.5 MiB", bytes(1024*1024*3/2))
	x.Equal("2.0 GiB", bytes(2<<30))
	x.Equal("1.0 TiB", bytes(1<<40))
	x.Equal(fmt.Sprintf("%.1f PiB", 1.0), bytes(1<<50))
}

// runIt drives the whole command against a configuration with one spool in it,
// which is the least that makes `status` and `forget` mean anything.
func runIt(t *testing.T, args ...string) xlitest.Result {
	t.Helper()

	dir := t.TempDir()
	p := filepath.Join(dir, "wick.yaml")

	body := "journal:\n  path: " + filepath.Join(dir, "wick.db") + "\n" +
		"sinks:\n  cloud: {type: dir, path: " + dir + "/cloud}\n" +
		"spools:\n  - name: recordings\n" +
		"    source: {type: dir, path: " + dir + "}\n" +
		"    to: [{sink: cloud}]\n"
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))

	return xlitest.Run(t, NewCmdRoot(), append([]string{"--config", p}, args...)...)
}

// refusingJournal answers everything with the same refusal, which is what a
// journal on a disk that has gone looks like from here.
type refusingJournal struct{ journal.Journal }

func (refusingJournal) Get(context.Context, string, string) (journal.Record, bool, error) {
	return journal.Record{}, false, errRefused
}

func (refusingJournal) Put(context.Context, journal.Record) error { return errRefused }

func (refusingJournal) Delete(context.Context, string, string) error { return errRefused }

func (refusingJournal) Range(context.Context, string) iter.Seq2[journal.Record, error] {
	return func(yield func(journal.Record, error) bool) { yield(journal.Record{}, errRefused) }
}

func (refusingJournal) Close() error { return nil }

// halfRefusingJournal answers about a key and then will not forget it.
type halfRefusingJournal struct{ journal.Journal }

func (halfRefusingJournal) Get(context.Context, string, string) (journal.Record, bool, error) {
	return journal.Record{State: journal.Carried}, true, nil
}

func (halfRefusingJournal) Delete(context.Context, string, string) error { return errRefused }

func (halfRefusingJournal) Close() error { return nil }

// These are the commands somebody runs when something is already wrong, so what
// they do when the records cannot be read is the whole of what they are for.
func TestWhenTheRecordsCannotBeRead(t *testing.T) {
	swap := func(t *testing.T, j journal.Journal) {
		t.Helper()

		was := openJournal
		openJournal = func(string) (journal.Journal, error) { return j, nil }
		t.Cleanup(func() { openJournal = was })
	}

	t.Run("status says so rather than saying nothing", func(t *testing.T) {
		x := require.New(t)

		swap(t, refusingJournal{})

		x.ErrorIs(runIt(t, "status").Err, errRefused)
		x.ErrorIs(runIt(t, "status", "--quarantined").Err, errRefused)
	})

	t.Run("forget says so rather than saying it forgot", func(t *testing.T) {
		x := require.New(t)

		swap(t, refusingJournal{})

		x.ErrorIs(runIt(t, "forget", "recordings", "a.rec").Err, errRefused)
	})

	t.Run("and so does one that answers and will not forget", func(t *testing.T) {
		x := require.New(t)

		swap(t, halfRefusingJournal{})

		x.ErrorIs(runIt(t, "forget", "recordings", "a.rec").Err, errRefused)
	})

	t.Run("one that cannot be opened at all is said so by both", func(t *testing.T) {
		x := require.New(t)

		was := openJournal
		openJournal = func(string) (journal.Journal, error) { return nil, errRefused }
		t.Cleanup(func() { openJournal = was })

		x.ErrorIs(runIt(t, "status").Err, errRefused)
		x.ErrorIs(runIt(t, "forget", "recordings", "a.rec").Err, errRefused)
	})
}

// A listener that stops listening is a thing that happens to a machine, and the
// probes not being answered has to say so rather than end in silence.
func TestAListenerThatGivesUp(t *testing.T) {
	x := require.New(t)

	was := listen
	listen = func(network, addr string) (net.Listener, error) {
		ln, err := was(network, addr)
		if err != nil {
			return nil, err
		}

		return refusingListener{Listener: ln}, nil
	}
	t.Cleanup(func() { listen = was })

	_, stop, err := serveHealth(t.Context(), "127.0.0.1:0", journalmem.New())
	x.NoError(err)
	defer stop()

	// The serve goroutine has to have got as far as being refused.
	time.Sleep(50 * time.Millisecond)
}

type refusingListener struct{ net.Listener }

func (refusingListener) Accept() (net.Conn, error) { return nil, errRefused }

// The bootstrap is the whole of what a command does before it does anything,
// and each of the ways it can refuse leaves the app not started rather than
// half started.
func TestTheBootstrap(t *testing.T) {
	t.Run("telemetry the configuration describes wrongly is refused", func(t *testing.T) {
		x := require.New(t)

		x.ErrorContains(runWith(t,
			"otel:\n  providers:\n    logger:\n      exporters: [absent]\n",
			"config",
		).Err, "build otel")
	})

	t.Run("telemetry that cannot be started is refused", func(t *testing.T) {
		x := require.New(t)

		was := startOtel
		startOtel = func(context.Context, *otx.Otx) error { return errRefused }
		t.Cleanup(func() { startOtel = was })

		x.ErrorContains(runWith(t, "", "config").Err, "start otel")
	})

	// Reading it twice would be two of everything it opens, and the second
	// would quietly win.
	t.Run("a configuration that is already read is not read again", func(t *testing.T) {
		x := require.New(t)

		ctx, _, err := UseConfigInit(t.Context(), NewCmdRoot())
		x.NoError(err)

		_, _, err = UseConfigInit(ctx, NewCmdRoot())
		x.ErrorContains(err, "already in context")
	})
}

// runWith drives the whole command against a configuration of the given body.
func runWith(t *testing.T, body string, args ...string) xlitest.Result {
	t.Helper()

	p := filepath.Join(t.TempDir(), "wick.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))

	return xlitest.Run(t, NewCmdRoot(), append([]string{"--config", p}, args...)...)
}

// summarisingJournal answers the counting and then will not be walked again,
// which is what a disk that goes between two reads looks like.
type summarisingJournal struct {
	journal.Journal

	ranges int
}

func (j *summarisingJournal) Range(ctx context.Context, source string) iter.Seq2[journal.Record, error] {
	j.ranges++
	if j.ranges > 1 {
		return func(yield func(journal.Record, error) bool) { yield(journal.Record{}, errRefused) }
	}

	return func(yield func(journal.Record, error) bool) {
		yield(journal.Record{Key: "a.rec", State: journal.Quarantined}, nil)
	}
}

func (*summarisingJournal) Close() error { return nil }

// Counting them and listing them are two walks, and the second can fail on its
// own.
func TestWhenTheListingCannotBeMade(t *testing.T) {
	x := require.New(t)

	was := openJournal
	openJournal = func(string) (journal.Journal, error) { return &summarisingJournal{}, nil }
	t.Cleanup(func() { openJournal = was })

	x.ErrorIs(runIt(t, "status", "--quarantined").Err, errRefused)
}

// The JSON form walks the journal the same two times the text form does, and
// each of them can fail on its own.
func TestJSONWhenTheJournalWillNotAnswer(t *testing.T) {
	t.Run("the counting", func(t *testing.T) {
		x := require.New(t)

		was := openJournal
		openJournal = func(string) (journal.Journal, error) { return refusingJournal{}, nil }
		t.Cleanup(func() { openJournal = was })

		x.ErrorIs(runIt(t, "status", "--json").Err, errRefused)
	})
	t.Run("the listing", func(t *testing.T) {
		x := require.New(t)

		was := openJournal
		openJournal = func(string) (journal.Journal, error) { return &summarisingJournal{}, nil }
		t.Cleanup(func() { openJournal = was })

		x.ErrorIs(runIt(t, "status", "--json", "--quarantined").Err, errRefused)
	})
}

// mixedJournal holds one that was set aside and one that was not, so that the
// listing has something to leave out.
type mixedJournal struct{ journal.Journal }

func (mixedJournal) Range(context.Context, string) iter.Seq2[journal.Record, error] {
	return func(yield func(journal.Record, error) bool) {
		if !yield(journal.Record{Key: "fine.rec", State: journal.Carried}, nil) {
			return
		}
		yield(journal.Record{Key: "a.rec", State: journal.Quarantined, Err: "it would not go"}, nil)
	}
}

func (mixedJournal) Close() error { return nil }

// --quarantined lists the ones that were set aside, and only those.
func TestJSONListsOnlyWhatWasSetAside(t *testing.T) {
	x := require.New(t)

	was := openJournal
	openJournal = func(string) (journal.Journal, error) { return mixedJournal{}, nil }
	t.Cleanup(func() { openJournal = was })

	r := runIt(t, "status", "--json", "--quarantined")
	x.NoError(r.Err)
	x.Contains(r.Stdout, "a.rec")
	x.Contains(r.Stdout, "it would not go")
	x.NotContains(r.Stdout, "fine.rec")
}
