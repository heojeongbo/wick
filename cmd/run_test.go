package cmd_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/cmd"
)

// The daemon, the whole way through: it starts, answers probes, carries what
// is there, and stops when it is told to.
func TestRun(t *testing.T) {
	x := require.New(t)

	g := newRig(t, toBoth)
	g.add(t, "a.rec", "contents")

	// Port zero, because two of these running at once must not collide.
	body, err := os.ReadFile(g.config)
	x.NoError(err)
	x.NoError(os.WriteFile(g.config, append(body, []byte("health:\n  endpoint: 127.0.0.1:0\n")...), 0o600))

	done := make(chan xlitest.Result, 1)
	go func() { done <- g.run(t, "run") }()

	// It carries what was there without waiting for an interval, which is what
	// a daemon starting on a machine holding a week of files has to do.
	until(t, "the first pass", func() bool {
		_, err := os.Stat(filepath.Join(g.onsite, "a.rec"))

		return err == nil
	})

	x.NoError(syscall.Kill(os.Getpid(), syscall.SIGTERM))

	select {
	case r := <-done:
		x.NoError(r.Err)

	case <-time.After(10 * time.Second):
		t.Fatal("it did not stop when it was told to")
	}
}

func TestRunRefusesWhatItCannotOpen(t *testing.T) {
	t.Run("a journal it cannot make", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()
		blocked := filepath.Join(dir, "blocked")
		x.NoError(os.WriteFile(blocked, nil, 0o600))

		p := filepath.Join(dir, "wick.yaml")
		x.NoError(os.WriteFile(p, []byte("journal:\n  path: "+filepath.Join(blocked, "a", "wick.db")+"\n"), 0o600))

		r := xlitest.Run(t, cmd.NewCmdRoot(), "--config", p, "run")
		x.ErrorContains(r.Err, "make the directory")
	})

	t.Run("an address it cannot listen on", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		body, err := os.ReadFile(g.config)
		x.NoError(err)
		x.NoError(os.WriteFile(g.config, append(body, []byte("health:\n  endpoint: 256.256.256.256:1\n")...), 0o600))

		x.ErrorContains(g.run(t, "run").Err, "listen for probes")
	})
}

// until waits for something to become true rather than hanging when it does not.
func until(t *testing.T, why string, f func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}

	t.Fatalf("waited for %s and it did not happen", why)
}
