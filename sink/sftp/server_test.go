package sftp_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// A real SSH server, speaking real SFTP, over a real socket.
//
// The narrow [sftp.Client] interface is what the failures are tested through;
// this is what says the interface is one a real connection satisfies, and that
// the host key checking, the key reading and the rename are what they claim to
// be against something that answers the way a server does.
type testServer struct {
	addr string
	dir  string

	// hostKey is what it presents, so that a test can write a known_hosts that
	// names it -- or one that names something else.
	hostKey ssh.Signer
	// user and password are what it lets in.
	user     string
	password string
	// authorized is a public key it also lets in.
	authorized ssh.PublicKey

	// refuseSubsystem makes it let the connection up and then refuse to speak
	// SFTP on it, which is what a server with the subsystem turned off does.
	refuseSubsystem bool
}

func startServer(t *testing.T) *testServer {
	t.Helper()

	x := require.New(t)

	_, key, err := ed25519.GenerateKey(rand.Reader)
	x.NoError(err)

	signer, err := ssh.NewSignerFromKey(key)
	x.NoError(err)

	s := &testServer{
		dir:      t.TempDir(),
		hostKey:  signer,
		user:     "wick",
		password: "sesame",
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	t.Cleanup(func() { _ = ln.Close() })

	s.addr = ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()

	return s
}

func (s *testServer) serve(conn net.Conn) {
	defer conn.Close()

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == s.user && string(pass) == s.password {
				return nil, nil
			}

			return nil, fmt.Errorf("no")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if s.authorized != nil && string(key.Marshal()) == string(s.authorized.Marshal()) {
				return nil, nil
			}

			return nil, fmt.Errorf("no")
		},
	}
	cfg.AddHostKey(s.hostKey)

	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()

	go ssh.DiscardRequests(reqs)

	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "only sessions here")

			continue
		}

		c, requests, err := ch.Accept()
		if err != nil {
			return
		}

		go func() {
			for r := range requests {
				_ = r.Reply(r.Type == "subsystem" && !s.refuseSubsystem, nil)
			}
		}()

		if s.refuseSubsystem {
			_ = c.Close()

			continue
		}

		srv, err := sftp.NewServer(c, sftp.WithServerWorkingDirectory(s.dir))
		if err != nil {
			return
		}
		_ = srv.Serve()
		_ = srv.Close()
	}
}

// knownHosts writes a known_hosts naming this server, which is what a
// deployment that has been told about it holds.
func (s *testServer) knownHosts(t *testing.T) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), "known_hosts")
	line := fmt.Sprintf("[%s]:%s %s\n",
		host(s.addr), port(s.addr), string(ssh.MarshalAuthorizedKey(s.hostKey.PublicKey())))
	require.NoError(t, os.WriteFile(p, []byte(line), 0o600))

	return p
}

// keyPair writes a private key and tells the server to let it in.
func (s *testServer) keyPair(t *testing.T, passphrase string) string {
	t.Helper()

	x := require.New(t)

	_, key, err := ed25519.GenerateKey(rand.Reader)
	x.NoError(err)

	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(key, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte(passphrase))
	}
	x.NoError(err)

	signer, err := ssh.NewSignerFromKey(key)
	x.NoError(err)
	s.authorized = signer.PublicKey()

	p := filepath.Join(t.TempDir(), "id_ed25519")
	x.NoError(os.WriteFile(p, pem.EncodeToMemory(block), 0o600))

	return p
}

func host(addr string) string {
	h, _, _ := net.SplitHostPort(addr) //nolint:errcheck // it is one this test made

	return h
}

func port(addr string) string {
	_, p, _ := net.SplitHostPort(addr) //nolint:errcheck // it is one this test made

	return p
}
