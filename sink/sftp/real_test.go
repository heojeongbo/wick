package sftp_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/sink"
	wicksftp "github.com/heojeongbo/wick/sink/sftp"
)

// Against a real SSH server speaking real SFTP over a real socket.
//
// The fake next door is what the failures are tested through. This is what says
// the narrow interface is one a real connection satisfies, and that the host key
// checking, the key reading and the rename mean what they claim to against
// something that answers the way a server does.
func TestAgainstARealServer(t *testing.T) {
	t.Run("a password, a known host, and a file that arrives", func(t *testing.T) {
		x := require.New(t)

		srv := startServer(t)
		s, err := wicksftp.New(wicksftp.Options{
			Address:    srv.addr,
			User:       srv.user,
			Password:   srv.password,
			KnownHosts: srv.knownHosts(t),
		})
		x.NoError(err)
		defer s.Close()

		x.NoError(s.Put(t.Context(), "a/b.rec", bytes.NewReader([]byte("contents")), sink.Meta{Size: 8}))

		b, err := os.ReadFile(filepath.Join(srv.dir, "a", "b.rec"))
		x.NoError(err)
		x.Equal("contents", string(b))

		// And nothing left beside it: the rename is what makes the write
		// atomic to anything watching that directory.
		es, err := os.ReadDir(filepath.Join(srv.dir, "a"))
		x.NoError(err)
		x.Len(es, 1)

		m, err := s.Stat(t.Context(), "a/b.rec")
		x.NoError(err)
		x.Equal(int64(8), m.Size)
	})

	t.Run("a key, and one that is locked", func(t *testing.T) {
		for _, passphrase := range []string{"", "sesame"} {
			t.Run(passphrase, func(t *testing.T) {
				x := require.New(t)

				srv := startServer(t)
				s, err := wicksftp.New(wicksftp.Options{
					Address:       srv.addr,
					User:          srv.user,
					KeyFile:       srv.keyPair(t, passphrase),
					KeyPassphrase: passphrase,
					KnownHosts:    srv.knownHosts(t),
				})
				x.NoError(err)
				defer s.Close()

				x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
				x.FileExists(filepath.Join(srv.dir, "a.rec"))
			})
		}
	})

	t.Run("a name it already holds is written over", func(t *testing.T) {
		x := require.New(t)

		srv := startServer(t)
		s, err := wicksftp.New(wicksftp.Options{
			Address: srv.addr, User: srv.user, Password: srv.password,
			KnownHosts: srv.knownHosts(t),
		})
		x.NoError(err)
		defer s.Close()

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("first")), sink.Meta{Size: 5}))
		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("second and longer")), sink.Meta{Size: 17}))

		b, err := os.ReadFile(filepath.Join(srv.dir, "a.rec"))
		x.NoError(err)
		x.Equal("second and longer", string(b))
	})

	t.Run("what it does not hold is not-there", func(t *testing.T) {
		x := require.New(t)

		srv := startServer(t)
		s, err := wicksftp.New(wicksftp.Options{
			Address: srv.addr, User: srv.user, Password: srv.password,
			KnownHosts: srv.knownHosts(t),
		})
		x.NoError(err)
		defer s.Close()

		_, err = s.Stat(t.Context(), "nothing.rec")
		x.ErrorIs(err, fs.ErrNotExist)
	})

	// The point of requiring known_hosts at all.
	t.Run("a server whose key is not the one that was written down is refused", func(t *testing.T) {
		x := require.New(t)

		srv := startServer(t)
		other := startServer(t)

		s, err := wicksftp.New(wicksftp.Options{
			Address: srv.addr, User: srv.user, Password: srv.password,
			// The file names the other server's key.
			KnownHosts: other.knownHosts(t),
		})
		x.NoError(err)
		defer s.Close()

		err = s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorContains(err, "reach")
	})

	t.Run("and one nobody wrote down at all is let in when that was asked for", func(t *testing.T) {
		x := require.New(t)

		srv := startServer(t)
		s, err := wicksftp.New(wicksftp.Options{
			Address: srv.addr, User: srv.user, Password: srv.password,
			InsecureIgnoreHostKey: true,
		})
		x.NoError(err)
		defer s.Close()

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
	})

	t.Run("a password that is not the password is refused", func(t *testing.T) {
		x := require.New(t)

		srv := startServer(t)
		s, err := wicksftp.New(wicksftp.Options{
			Address: srv.addr, User: srv.user, Password: "not it",
			KnownHosts: srv.knownHosts(t),
		})
		x.NoError(err)
		defer s.Close()

		err = s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorContains(err, "reach")
	})

	t.Run("a server that is not there is said so", func(t *testing.T) {
		x := require.New(t)

		s, err := wicksftp.New(wicksftp.Options{
			Address: "127.0.0.1:1", User: "wick", Password: "sesame",
			InsecureIgnoreHostKey: true,
		})
		x.NoError(err)
		defer s.Close()

		err = s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorContains(err, "reach")
	})
}

// The things that are refused before anything is dialled.
func TestWhatIsRefusedBeforeAnythingIsSent(t *testing.T) {
	t.Run("a key that is not there", func(t *testing.T) {
		x := require.New(t)

		srv := startServer(t)
		s, err := wicksftp.New(wicksftp.Options{
			Address: srv.addr, User: srv.user,
			KeyFile:    filepath.Join(t.TempDir(), "nope"),
			KnownHosts: srv.knownHosts(t),
		})
		x.NoError(err)
		defer s.Close()

		err = s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorContains(err, "read the key")
	})

	t.Run("a key that is not a key", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "id")
		require.NoError(t, os.WriteFile(p, []byte("not a key"), 0o600))

		srv := startServer(t)
		s, err := wicksftp.New(wicksftp.Options{
			Address: srv.addr, User: srv.user, KeyFile: p,
			KnownHosts: srv.knownHosts(t),
		})
		x.NoError(err)
		defer s.Close()

		err = s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorContains(err, "read the key")
	})

	t.Run("a passphrase that is not the passphrase", func(t *testing.T) {
		x := require.New(t)

		srv := startServer(t)
		s, err := wicksftp.New(wicksftp.Options{
			Address: srv.addr, User: srv.user,
			KeyFile:       srv.keyPair(t, "sesame"),
			KeyPassphrase: "not it",
			KnownHosts:    srv.knownHosts(t),
		})
		x.NoError(err)
		defer s.Close()

		err = s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorContains(err, "read the key")
	})

	t.Run("a known_hosts that is not there", func(t *testing.T) {
		x := require.New(t)

		srv := startServer(t)
		s, err := wicksftp.New(wicksftp.Options{
			Address: srv.addr, User: srv.user, Password: srv.password,
			KnownHosts: filepath.Join(t.TempDir(), "nope"),
		})
		x.NoError(err)
		defer s.Close()

		err = s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorContains(err, "read the known hosts")
	})
}

// The connection comes up and the server will not speak SFTP on it, which is
// what one with the subsystem turned off does.
func TestAServerThatWillNotSpeakSftp(t *testing.T) {
	x := require.New(t)

	srv := startServer(t)
	srv.refuseSubsystem = true

	s, err := wicksftp.New(wicksftp.Options{
		Address: srv.addr, User: srv.user, Password: srv.password,
		KnownHosts: srv.knownHosts(t),
	})
	x.NoError(err)
	defer s.Close()

	err = s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
	x.ErrorContains(err, "reach")
}

// Something is already in the way of the name it writes beside.
func TestAPlaceItCannotWriteBeside(t *testing.T) {
	x := require.New(t)

	srv := startServer(t)
	x.NoError(os.Mkdir(filepath.Join(srv.dir, ".a.rec.partial"), 0o755))

	s, err := wicksftp.New(wicksftp.Options{
		Address: srv.addr, User: srv.user, Password: srv.password,
		KnownHosts: srv.knownHosts(t),
	})
	x.NoError(err)
	defer s.Close()

	err = s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
	x.ErrorContains(err, "make")
}
