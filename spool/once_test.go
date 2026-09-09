package spool_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/journal"
	journalmem "github.com/heojeongbo/wick/journal/mem"
	"github.com/heojeongbo/wick/retain"
)

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))

	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestOnce(t *testing.T) {
	t.Run("what is found is carried, named, and written down", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t)
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.NoError(r.Err())
		x.Equal(1, r.Scanned)
		x.Equal(1, r.Settled)
		x.Equal(1, r.Carried)
		x.Equal(int64(8), r.Bytes)

		// Named by the template, which is what keeps three machines writing
		// into one bucket from writing over each other.
		b, ok := g.cloud.Data("thor-top/a.rec")
		x.True(ok)
		x.Equal("contents", string(b))

		rec, ok := g.record(t, "a.rec")
		x.True(ok)
		x.Equal(journal.Retired, rec.State)
		x.True(rec.IsCarriedTo("cloud"))
		x.Equal("thor-top/a.rec", rec.Carried["cloud"].Name)
		// Hashed on the way past, so that the one read pays for both the
		// sending and the checking.
		x.Equal(sha256Of("contents"), rec.Digest)
		x.Equal(rec.Digest, rec.Carried["cloud"].Digest)
	})

	// A re-run with nothing new must cost a few questions and no bytes. It is
	// the property most worth an explicit check, because getting it wrong is
	// invisible until the bill arrives.
	t.Run("a second pass with nothing new sends nothing", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t)
		g.add("a.rec", "contents")

		_, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, g.cloud.Puts())

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Carried)
		x.Zero(r.Bytes)
		x.Equal(1, g.cloud.Puts())
	})

	t.Run("a file written again under the same name is a different file", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t)
		g.add("a.rec", "first")

		_, err := g.Once(t.Context())
		x.NoError(err)

		// Same name, other contents, later.
		g.src.Add("a.rec", []byte("second"), start.Add(time.Hour))

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)

		b, _ := g.cloud.Data("thor-top/a.rec")
		x.Equal("second", string(b))
	})

	t.Run("nothing to carry is not a failure", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t)
		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Scanned)
		x.Zero(r.Carried)
	})

	t.Run("a scan that cannot be made at all ends the pass", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t)
		g.src.FailScan(errRefused)

		_, err := g.Once(t.Context())
		x.ErrorIs(err, errRefused)
	})

	t.Run("one entry that cannot be looked at does not hide the rest", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t)
		g.add("a.rec", "aaa")
		g.add("b.rec", "bbb")
		g.src.FailItem("a.rec", errRefused)

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)
		x.ErrorIs(r.Err(), errRefused)
	})
}

// Every destination has to answer. That is what makes "the cloud and the server
// in the building" one arrangement rather than two half-arrangements.
func TestEveryDestinationHasToAnswer(t *testing.T) {
	t.Run("both of them, and only then is it written down", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, twoDestinations)
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)
		// The same bytes, twice, because there are two places for them to be.
		x.Equal(int64(16), r.Bytes)

		_, ok := g.cloud.Data("thor-top/a.rec")
		x.True(ok)
		_, ok = g.onsite.Data("a.rec")
		x.True(ok)

		rec, _ := g.record(t, "a.rec")
		x.True(rec.IsCarriedTo("cloud"))
		x.True(rec.IsCarriedTo("onsite"))
	})

	// A restart in the middle of a fan-out must not send again to the one that
	// already answered. On a metered link that is the difference between a
	// retry and a bill.
	t.Run("the one that already answered is not sent to again", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, twoDestinations)
		g.add("a.rec", "contents")
		g.onsite.FailPut("a.rec", errRefused)

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Carried)
		x.ErrorIs(r.Err(), errRefused)

		// The cloud has it, and the journal remembers that it does.
		x.Equal(1, g.cloud.Puts())
		rec, _ := g.record(t, "a.rec")
		x.Equal(journal.Pending, rec.State)
		x.True(rec.IsCarriedTo("cloud"))
		x.False(rec.IsCarriedTo("onsite"))

		// The next pass sends to the one that has not answered, and to no
		// other.
		g.onsite.Unswallow("a.rec")
		r, err = g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)
		x.Equal(1, g.cloud.Puts())
		x.Equal(1, g.onsite.Puts())
	})
}

// A store that answers a write with a success it does not keep is not a
// hypothetical, and the read-back is the only thing that catches it.
func TestVerify(t *testing.T) {
	t.Run("a write that was swallowed is not recorded as a carry", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t)
		g.add("a.rec", "contents")
		g.cloud.Swallow("thor-top/a.rec")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Carried)
		x.ErrorContains(r.Err(), "ask")

		rec, ok := g.record(t, "a.rec")
		x.True(ok)
		x.Equal(journal.Pending, rec.State)
		x.False(rec.IsCarriedTo("cloud"))

		// And the file is still where it was, which is the whole point.
		x.Equal([]string{"a.rec"}, g.src.Keys())
	})

	t.Run("a read-back that is refused is not read as a carry either", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t)
		g.add("a.rec", "contents")
		g.cloud.FailStat("thor-top/a.rec", errRefused)

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Carried)
		x.ErrorIs(r.Err(), errRefused)
	})

	t.Run("not asking for it is allowed, and then the write is believed", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, noVerify)
		g.add("a.rec", "contents")
		g.cloud.Swallow("thor-top/a.rec")

		r, err := g.Once(t.Context())
		x.NoError(err)
		// Believed, and wrongly. Which is why verify is the default in the
		// configuration and why turning it off is a decision somebody wrote
		// down.
		x.Equal(1, r.Carried)
	})
}

// A file that is still being written looks exactly like a file that has been
// written. The only way to tell is to look twice.
func TestSettle(t *testing.T) {
	t.Run("something seen for the first time is not carried yet", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, withSettle(10*time.Second))
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Scanned)
		x.Zero(r.Settled)
		x.Zero(g.cloud.Puts())
	})
	t.Run("one that has sat still long enough is", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, withSettle(10*time.Second))
		g.add("a.rec", "contents")

		_, err := g.Once(t.Context())
		x.NoError(err)

		g.clock.tick(11 * time.Second)
		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)
	})
	t.Run("one that is still growing has its clock started again", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, withSettle(10*time.Second))
		g.add("a.rec", "half")

		_, err := g.Once(t.Context())
		x.NoError(err)

		g.clock.tick(9 * time.Second)
		g.src.Add("a.rec", []byte("half and more"), start.Add(9*time.Second))

		// Nine seconds after it was last written is not ten.
		g.clock.tick(9 * time.Second)
		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Settled)

		g.clock.tick(2 * time.Second)
		r, err = g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)

		b, _ := g.cloud.Data("thor-top/a.rec")
		x.Equal("half and more", string(b))
	})
	t.Run("without one, things are carried as soon as they are seen", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t)
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)
	})
	t.Run("what is gone is forgotten, so a long run does not remember everything that ever was", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, withSettle(10*time.Second), withRetain(retain.Delete()))
		g.add("a.rec", "contents")

		_, err := g.Once(t.Context())
		x.NoError(err)

		g.clock.tick(11 * time.Second)
		_, err = g.Once(t.Context())
		x.NoError(err)
		x.Empty(g.src.Keys())

		// It is put back, and has to sit still all over again rather than
		// being carried at once on the strength of a sighting from before.
		g.src.Add("a.rec", []byte("again"), start.Add(time.Hour))
		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Settled)
	})
}

func TestRetain(t *testing.T) {
	t.Run("told nothing, it keeps the file", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t)
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Retired)
		x.Equal([]string{"a.rec"}, g.src.Keys())
	})
	t.Run("told to delete, it deletes", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, withRetain(retain.Delete()))
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Retired)
		x.Empty(g.src.Keys())
	})

	// A restart in the window between the record and the retirement is
	// otherwise a file that is never tidied: it is not in the scan's way any
	// more, so nothing else would ever look at it again.
	t.Run("what was carried and not yet retired is picked up on the next pass", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, withRetain(retain.Grace(24*time.Hour, retain.Delete())))
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)
		x.Zero(r.Retired)
		x.Equal([]string{"a.rec"}, g.src.Keys())

		g.clock.tick(25 * time.Hour)
		r, err = g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Carried)
		x.Equal(1, r.Retired)
		x.Empty(g.src.Keys())
	})

	t.Run("a retirement that is refused is said so and tried again", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, withRetain(retain.Delete()))
		g.add("a.rec", "contents")
		g.src.FailRemove("a.rec", errRefused)

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)
		x.Zero(r.Retired)
		x.ErrorIs(r.Err(), errRefused)

		rec, _ := g.record(t, "a.rec")
		x.Equal(journal.Carried, rec.State)
	})

	// A filesystem that cannot be measured must not read as one that is full.
	t.Run("a policy that asks about room is given none when nobody could say", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t,
			withFree(0, errRefused),
			withRetain(retain.WhenFreeBelow(1<<30, retain.Delete())),
		)
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)
		x.Zero(r.Retired)
		x.Equal([]string{"a.rec"}, g.src.Keys())
	})
	t.Run("and the real answer when somebody could", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t,
			withFree(1024, nil),
			withRetain(retain.WhenFreeBelow(1<<30, retain.Delete())),
		)
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Retired)
		x.Empty(g.src.Keys())
	})
}

// One item that cannot be carried must not spend the whole link on itself while
// everything behind it waits.
func TestSetAside(t *testing.T) {
	x := require.New(t)

	g := newRig(t, withAttempts(2))
	g.add("a.rec", "contents")
	g.add("b.rec", "other")
	g.cloud.FailPut("thor-top/a.rec", errRefused)

	r, err := g.Once(t.Context())
	x.NoError(err)
	x.Equal(1, r.Carried) // b.rec
	x.Zero(r.SetAside)

	rec, _ := g.record(t, "a.rec")
	x.Equal(1, rec.Attempts)
	x.Equal(journal.Pending, rec.State)

	r, err = g.Once(t.Context())
	x.NoError(err)
	x.Equal(1, r.SetAside)

	rec, _ = g.record(t, "a.rec")
	x.Equal(journal.Quarantined, rec.State)
	x.Contains(rec.Err, "refused")

	// And it stops being tried, so the pass is quiet from here on.
	r, err = g.Once(t.Context())
	x.NoError(err)
	x.Zero(r.Settled)
	x.NoError(r.Err())

	// Until it becomes a different file, which is not the file that was set
	// aside.
	g.src.Add("a.rec", []byte("something else"), start.Add(time.Hour))
	g.cloud.Unswallow("thor-top/a.rec")

	r, err = g.Once(t.Context())
	x.NoError(err)
	x.Equal(1, r.Carried)
}

func TestSomethingThatGoesAway(t *testing.T) {
	x := require.New(t)

	g := newRig(t)
	g.add("a.rec", "contents")
	// Found by the scan, gone by the time it is opened, which is what a
	// competing cleanup looks like.
	g.src.FailOpen("a.rec", &fs.PathError{Op: "open", Path: "a.rec", Err: fs.ErrNotExist})

	r, err := g.Once(t.Context())
	x.NoError(err)
	x.Zero(r.Carried)
	x.NoError(r.Err())

	// Nothing is remembered about it: it is not a failure to count against it,
	// and there is nothing to carry.
	_, ok := g.record(t, "a.rec")
	x.False(ok)
}

func TestWorkers(t *testing.T) {
	x := require.New(t)

	g := newRig(t, withWorkers(4))
	for _, k := range []string{"a.rec", "b.rec", "c.rec", "d.rec", "e.rec"} {
		g.add(k, "contents of "+k)
	}

	r, err := g.Once(t.Context())
	x.NoError(err)
	x.Equal(5, r.Carried)
	x.Len(g.cloud.Names(), 5)
}

// A name made out of the hash cannot be known until the file has been read all
// the way through, so it is read twice. That is the cost of asking for it.
func TestANameMadeOutOfTheHash(t *testing.T) {
	x := require.New(t)

	g := newRig(t, withNaming("blob/{sha256}"))
	g.add("a.rec", "contents")

	r, err := g.Once(t.Context())
	x.NoError(err)
	x.Equal(1, r.Carried)

	rec, _ := g.record(t, "a.rec")
	x.Contains(g.cloud.Names()[0], "blob/")
	x.Equal("blob/"+rec.Digest[len("sha256:"):], g.cloud.Names()[0])
}

func TestANameThatCannotBeMade(t *testing.T) {
	x := require.New(t)

	// "{dir}" of a key with no directory in it leaves nothing to call it by.
	g := newRig(t, withNaming("{dir}"))
	g.add("a.rec", "contents")

	r, err := g.Once(t.Context())
	x.NoError(err)
	x.Zero(r.Carried)
	x.ErrorContains(r.Err(), "what to call")
}

func TestAContextThatIsDone(t *testing.T) {
	x := require.New(t)

	g := newRig(t)
	g.add("a.rec", "contents")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := g.Once(ctx)
	x.ErrorIs(err, ctx.Err())
	x.Zero(g.cloud.Puts())
}

func TestAHashThatChangedUnderneath(t *testing.T) {
	x := require.New(t)

	// The name asks for the hash, so the file is read twice -- and between the
	// two reads it becomes something else.
	g := newRig(t, withNaming("blob/{sha256}"))
	g.add("a.rec", "contents")

	// On the second read: the first is the one the name is made out of, so the
	// hash is of what was there then and the bytes sent are of what is there
	// now.
	g.src.SwapOnOpen("a.rec", 2, []byte("something else entirely"))

	r, err := g.Once(t.Context())
	x.NoError(err)
	x.Zero(r.Carried)
	x.ErrorContains(r.Err(), "changed while it was being carried")
}

func TestAJournalThatWillNotAnswer(t *testing.T) {
	t.Run("when it is asked what is known", func(t *testing.T) {
		x := require.New(t)

		jnl := &failingJournal{Journal: journalmem.New()}
		g := newRig(t, withJournal(jnl))
		g.add("a.rec", "contents")
		jnl.getErr = errRefused

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.ErrorIs(r.Err(), errRefused)
		x.Zero(g.cloud.Puts())
	})
	t.Run("when it is asked to remember a carry", func(t *testing.T) {
		x := require.New(t)

		jnl := &failingJournal{Journal: journalmem.New()}
		g := newRig(t, withJournal(jnl))
		g.add("a.rec", "contents")
		jnl.putErr = errRefused

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Carried)
		x.ErrorIs(r.Err(), errRefused)
		// The copy is there and nothing says so. The next pass sends it again,
		// which costs the bytes and loses nothing.
		x.Equal(1, g.cloud.Puts())
	})
	t.Run("when it is asked what has already been carried", func(t *testing.T) {
		x := require.New(t)

		jnl := &failingJournal{Journal: journalmem.New()}
		g := newRig(t, withJournal(jnl))
		jnl.rangeErr = errRefused

		_, err := g.Once(t.Context())
		x.ErrorIs(err, errRefused)
	})
	t.Run("when it is asked to forget something that went away", func(t *testing.T) {
		x := require.New(t)

		jnl := &failingJournal{Journal: journalmem.New()}
		g := newRig(t, withJournal(jnl))
		g.add("a.rec", "contents")
		g.src.FailOpen("a.rec", &fs.PathError{Op: "open", Path: "a.rec", Err: fs.ErrNotExist})
		jnl.deleteErr = errRefused

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.ErrorIs(r.Err(), errRefused)
	})
}

// `wick once` is a fresh process every time something runs it. A gate that only
// knew what this process had watched would say "not yet" on every single run --
// a one-shot that can never carry anything, which is the shape most schedulers
// use.
func TestSettlingSurvivesARestart(t *testing.T) {
	x := require.New(t)

	g := newRig(t, withSettle(10*time.Second))
	// Written a minute ago, and this process has never seen it before.
	g.src.Add("a.rec", []byte("contents"), start.Add(-time.Minute))

	r, err := g.Once(t.Context())
	x.NoError(err)
	x.Equal(1, r.Carried)
}

// A machine that boots without a network has its clock put right hours later.
// Until then its idea of now is behind every file it holds, and the file's own
// time says "written in the future" -- which must not mean "never carry
// anything".
func TestAClockThatIsBehindEverything(t *testing.T) {
	x := require.New(t)

	g := newRig(t, withSettle(10*time.Second))
	// The file says it was written an hour after now.
	g.src.Add("a.rec", []byte("contents"), start.Add(time.Hour))

	r, err := g.Once(t.Context())
	x.NoError(err)
	x.Zero(r.Settled)

	// What this process has watched happen is still true, so once it has
	// watched it sit still for long enough, it goes.
	g.clock.tick(11 * time.Second)
	r, err = g.Once(t.Context())
	x.NoError(err)
	x.Equal(1, r.Carried)
}
