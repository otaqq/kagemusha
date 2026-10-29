package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serveOn(t *testing.T, host string, code int) int {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("cannot listen on %s: %v", host, err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(code)
	}))
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return l.Addr().(*net.TCPAddr).Port
}

func TestProbe(t *testing.T) {
	port := serveOn(t, "127.0.0.1", http.StatusOK)
	for _, host := range []string{"", "0.0.0.0", "127.0.0.1"} {
		if err := Probe(context.Background(), host, port); err != nil {
			t.Errorf("%q: %v", host, err)
		}
	}
	if err := Probe(context.Background(), "127.0.0.1", serveOn(t, "127.0.0.1", http.StatusServiceUnavailable)); err == nil {
		t.Error("503 must fail")
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if err := Probe(context.Background(), "", closed); err == nil {
		t.Error("nothing listening must fail")
	}
	if err := Probe(context.Background(), "bad host", 1); err == nil {
		t.Error("invalid host must fail")
	}
}

func TestProbe_IPv6Wildcard(t *testing.T) {
	if err := Probe(context.Background(), "::", serveOn(t, "::1", http.StatusOK)); err != nil {
		t.Fatal(err)
	}
}
