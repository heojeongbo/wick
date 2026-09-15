package http_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/heojeongbo/wick/sink"
	wickhttp "github.com/heojeongbo/wick/sink/http"
)

// A server the fleet already runs. No SDK, no notion of a region: an address, a
// method, and whatever the far end wants to be told.
//
// The read-back is of the length unless the server keeps a hash and says so
// under a header of its choosing -- see digest_header in docs/SINKS.md.
func Example() {
	ctx := context.Background()

	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			user, pass, _ := r.BasicAuth()
			fmt.Printf("%s %s as %s/%s\n", r.Method, r.URL.Path, user, pass)

			b := &bytes.Buffer{}
			_, _ = b.ReadFrom(r.Body)
			got = b.Bytes()
			w.WriteHeader(http.StatusCreated)

		case http.MethodHead:
			w.Header().Set("Content-Length", fmt.Sprint(len(got)))
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	s, err := wickhttp.New(wickhttp.Options{
		Endpoint: srv.URL + "/drop",
		Method:   http.MethodPut,
		Auth: wickhttp.Auth{
			Username: "wick",
			// In a configuration this is "${env:COLLECTOR_PASSWORD}", so that
			// it is named in the file rather than held in it.
			Password: "hunter2",
		},
	})
	if err != nil {
		panic(err)
	}

	b := []byte("what the robot recorded")
	if err := s.Put(ctx, "thor-top/a.rec", bytes.NewReader(b), sink.Meta{Size: int64(len(b))}); err != nil {
		panic(err)
	}

	m, err := s.Stat(ctx, "thor-top/a.rec")
	if err != nil {
		panic(err)
	}
	fmt.Println("the server holds", m.Size, "bytes")

	// Output:
	// PUT /drop/thor-top/a.rec as wick/hunter2
	// the server holds 23 bytes
}

// A username and a token file together is refused, because both set
// Authorization and one of them would quietly win.
//
// Refused when the sink is built, which is startup, rather than at the first
// carry at three in the morning.
func ExampleNew_twoAnswersToOneQuestion() {
	_, err := wickhttp.New(wickhttp.Options{
		Endpoint: "https://collector.example.com/drop",
		Auth: wickhttp.Auth{
			Username:  "wick",
			TokenFile: "/var/run/secrets/token",
		},
	})

	fmt.Println(err)

	// Output:
	// a username and a token file are two answers to the same question; give one
}
