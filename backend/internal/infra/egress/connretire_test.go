package egress

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	egressdomain "github.com/chenyme/grok2api/backend/internal/domain/egress"
)

func TestBuildLeaseDropsConnOn429And5xxButReusesOnSuccess(t *testing.T) {
	for _, test := range []struct {
		name  string
		first int
		body  string
		dials int32
	}{
		{name: "success reuses", first: http.StatusOK, body: "ok", dials: 1},
		{name: "429 dials again", first: http.StatusTooManyRequests, body: `{"error":"limited"}`, dials: 2},
		{name: "500 dials again", first: http.StatusBadGateway, body: `{"error":"upstream"}`, dials: 2},
		{name: "404 reuses", first: http.StatusNotFound, body: `{"error":"missing"}`, dials: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var hits atomic.Int32
			listener := &countingListener{}
			base, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			listener.Listener = base
			server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				if hits.Add(1) == 1 {
					writer.Header().Set("Content-Type", "application/json")
					writer.WriteHeader(test.first)
					_, _ = io.WriteString(writer, test.body)
					return
				}
				writer.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(writer, "second")
			})}
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			t.Cleanup(func() {
				_ = server.Close()
				<-done
			})

			client, err := newBuildClient("", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.CloseIdleConnections)
			lease := &Lease{client: client, Scope: egressdomain.ScopeBuild}
			firstBody := doLease(t, lease, "http://"+listener.Addr().String()+"/v1/responses")
			if firstBody != test.body {
				t.Fatalf("first body = %q", firstBody)
			}
			secondBody := doLease(t, lease, "http://"+listener.Addr().String()+"/v1/responses")
			if secondBody != "second" {
				t.Fatalf("second body = %q", secondBody)
			}
			if got := listener.n.Load(); got != test.dials {
				t.Fatalf("tcp dials = %d, want %d", got, test.dials)
			}
		})
	}
}

func TestBuildLeaseDropsHTTP2ConnOn429(t *testing.T) {
	var hits atomic.Int32
	listener := &countingListener{}
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Listener = base
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(writer, `{"error":"limited"}`)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "second")
	}))
	server.Listener = listener
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client, err := newBuildClient("", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	transport := client.Transport.(*http.Transport)
	transport.TLSClientConfig.InsecureSkipVerify = true
	lease := &Lease{client: client, Scope: egressdomain.ScopeBuild}
	first, err := http.NewRequest(http.MethodPost, server.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	response, err := lease.Do(first)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.ProtoMajor != 2 {
		t.Fatalf("proto = %s, want HTTP/2", response.Proto)
	}
	if string(payload) != `{"error":"limited"}` {
		t.Fatalf("first body = %q", payload)
	}
	if body := doLease(t, lease, server.URL); body != "second" {
		t.Fatalf("second body = %q", body)
	}
	if got := listener.n.Load(); got != 2 {
		t.Fatalf("tcp dials = %d, want 2", got)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestBrowserClientDropsConnOn429(t *testing.T) {
	var hits atomic.Int32
	listener := &countingListener{}
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Listener = base
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(writer, `{"error":"limited"}`)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "second")
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
	})

	client, err := newBrowserClient("", DefaultUserAgent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	url := "http://" + listener.Addr().String() + "/rest/app-chat"
	first := doBrowser(t, client, url)
	if first != `{"error":"limited"}` {
		t.Fatalf("first body = %q", first)
	}
	second := doBrowser(t, client, url)
	if second != "second" {
		t.Fatalf("second body = %q", second)
	}
	if got := listener.n.Load(); got != 2 {
		t.Fatalf("tcp dials = %d, want 2", got)
	}
}

func doLease(t *testing.T, lease *Lease, url string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	response, err := lease.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func doBrowser(t *testing.T, client *browserClient, url string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

type countingListener struct {
	net.Listener
	n atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.n.Add(1)
	}
	return conn, err
}
