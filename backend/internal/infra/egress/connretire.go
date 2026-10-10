package egress

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"

	fhttp "github.com/bogdanfinn/fhttp"
	fhttptrace "github.com/bogdanfinn/fhttp/httptrace"
)

// upstreamErrorBodyLimit is how much of a 429/5xx body we keep before closing
// the TCP connection. Error payloads are small; the cap only stops a hostile
// or truncated body from pinning the caller.
const upstreamErrorBodyLimit = 1 << 20

// WatchUpstreamConn records the TCP connection a net/http client uses for this
// request. Call the returned function with the response so a 429 or 5xx can
// close that connection after its body has been buffered.
func WatchUpstreamConn(request *http.Request) (*http.Request, func(*http.Response)) {
	if request == nil {
		return nil, func(*http.Response) {}
	}
	conn := &capturedConn{}
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		conn.store(info.Conn)
	}}
	ctx := httptrace.WithClientTrace(request.Context(), trace)
	ctx = context.WithValue(ctx, upstreamConnDropKey{}, conn.drop)
	return request.WithContext(ctx), conn.retire
}

// watchBrowserConn is the fhttp equivalent. tls-client does not observe a
// net/http trace stored on the original request context.
func watchBrowserConn(request *fhttp.Request) (*fhttp.Request, func(*http.Response)) {
	if request == nil {
		return nil, func(*http.Response) {}
	}
	conn := &capturedConn{}
	trace := &fhttptrace.ClientTrace{GotConn: func(info fhttptrace.GotConnInfo) {
		conn.store(info.Conn)
	}}
	return request.WithContext(fhttptrace.WithClientTrace(request.Context(), trace)), conn.retire
}

type capturedConn struct {
	mu   sync.Mutex
	conn net.Conn
}

func (c *capturedConn) store(conn net.Conn) {
	if c == nil || conn == nil {
		return
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
}

func (c *capturedConn) drop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// UpstreamConnDropper returns a function that closes the TCP captured for this
// response. Call it after a 200 stream ends without a terminal event, so the
// next request dials again. Nil when this response did not go through
// WatchUpstreamConn.
func UpstreamConnDropper(response *http.Response) func() {
	if response == nil || response.Request == nil {
		return nil
	}
	drop, _ := response.Request.Context().Value(upstreamConnDropKey{}).(func())
	return drop
}

type upstreamConnDropKey struct{}

func (c *capturedConn) retire(response *http.Response) {
	if c == nil {
		return
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	dropUpstreamConn(conn, response)
}

func dropUpstreamConn(conn net.Conn, response *http.Response) {
	if conn == nil || response == nil {
		return
	}
	if response.StatusCode != http.StatusTooManyRequests && response.StatusCode < http.StatusInternalServerError {
		return
	}
	// Buffer first. Callers still classify the 429/5xx JSON, and Body.Close
	// must finish while the TCP connection is alive (trailers, HTTP/2 stream
	// end). Closing afterwards takes this connection out of the pool; the next
	// request dials a new one. On HTTP/2 that TCP is shared, so other streams
	// on it fail too — that is the connection the caller asked to drop.
	payload := readUpstreamErrorBody(response.Body)
	if response.Body != nil {
		_ = response.Body.Close()
	}
	_ = conn.Close()
	response.Body = io.NopCloser(bytes.NewReader(payload))
	response.ContentLength = int64(len(payload))
	response.TransferEncoding = nil
}

func readUpstreamErrorBody(body io.ReadCloser) []byte {
	if body == nil {
		return nil
	}
	payload, _ := io.ReadAll(io.LimitReader(body, upstreamErrorBodyLimit))
	return payload
}
