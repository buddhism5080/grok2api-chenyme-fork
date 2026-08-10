package streamidle

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenyme/grok2api/backend/internal/pkg/neterror"
)

// Phase state machine for the streaming body wrapper.
// Transitions:
//
//	awaitFirst --(first byte)--> streaming --(idle timeout)--> idleTimedOut
//	awaitFirst --(first-char timeout)--> firstCharTimedOut
//	* --(Close)--> closed
//
// Pure-idle mode (firstChar <= 0) starts in streaming with the idle timer armed.
const (
	phaseAwaitFirst int32 = iota
	phaseStreaming
	phaseFirstCharTimedOut
	phaseIdleTimedOut
	phaseClosed
)

// ReadCloser enforces inactivity deadlines over one streaming response body.
// It cancels the owning request context so HTTP/1.1 and HTTP/2 transports can
// unblock an in-flight Read without applying a connection-wide deadline.
//
// When firstChar > 0, the first timer arms with ErrUpstreamFirstCharTimeout.
// After the first non-empty Read, that timer is stopped and an idle timer is
// armed with ErrUpstreamStreamIdleTimeout (if idle > 0). When firstChar <= 0,
// only the idle timer is used (legacy pure-idle behaviour for Web/Console).
type ReadCloser struct {
	io.ReadCloser
	firstChar time.Duration
	idle      time.Duration
	cancel    context.CancelCauseFunc

	phase    atomic.Int32
	observed atomic.Bool

	// timerMu protects firstTimer/idleTimer pointer swaps and Stop/Reset calls
	// against the AfterFunc callbacks and Close.
	timerMu    sync.Mutex
	firstTimer *time.Timer
	idleTimer  *time.Timer
}

// New creates a wrapper that enforces first-char and/or idle timeouts.
// Pass firstChar <= 0 to keep pure idle behaviour (Web/Console compatibility).
func New(body io.ReadCloser, firstChar, idle time.Duration, cancel context.CancelCauseFunc) *ReadCloser {
	wrapper := &ReadCloser{
		ReadCloser: body,
		firstChar:  firstChar,
		idle:       idle,
		cancel:     cancel,
	}
	if firstChar > 0 {
		wrapper.phase.Store(phaseAwaitFirst)
		wrapper.timerMu.Lock()
		wrapper.firstTimer = time.AfterFunc(firstChar, wrapper.onFirstCharTimeout)
		wrapper.timerMu.Unlock()
	} else {
		wrapper.phase.Store(phaseStreaming)
		if idle > 0 {
			wrapper.timerMu.Lock()
			wrapper.idleTimer = time.AfterFunc(idle, wrapper.onIdleTimeout)
			wrapper.timerMu.Unlock()
		}
	}
	return wrapper
}

func (r *ReadCloser) onFirstCharTimeout() {
	if !r.phase.CompareAndSwap(phaseAwaitFirst, phaseFirstCharTimedOut) {
		return
	}
	r.cancel(neterror.ErrUpstreamFirstCharTimeout)
}

func (r *ReadCloser) onIdleTimeout() {
	if !r.phase.CompareAndSwap(phaseStreaming, phaseIdleTimedOut) {
		return
	}
	r.cancel(neterror.ErrUpstreamStreamIdleTimeout)
}

func (r *ReadCloser) Read(buffer []byte) (int, error) {
	n, err := r.ReadCloser.Read(buffer)
	if n > 0 {
		r.noteProgress()
	}
	if err != nil {
		if cause := r.timeoutCause(); cause != nil {
			return n, cause
		}
	}
	return n, err
}

func (r *ReadCloser) noteProgress() {
	r.observed.Store(true)
	switch r.phase.Load() {
	case phaseAwaitFirst:
		if !r.phase.CompareAndSwap(phaseAwaitFirst, phaseStreaming) {
			return
		}
		r.timerMu.Lock()
		if r.firstTimer != nil {
			r.firstTimer.Stop()
			r.firstTimer = nil
		}
		if r.idle > 0 && r.idleTimer == nil {
			r.idleTimer = time.AfterFunc(r.idle, r.onIdleTimeout)
		}
		r.timerMu.Unlock()
	case phaseStreaming:
		r.timerMu.Lock()
		if r.idleTimer != nil {
			r.idleTimer.Reset(r.idle)
		}
		r.timerMu.Unlock()
	}
}

func (r *ReadCloser) timeoutCause() error {
	switch r.phase.Load() {
	case phaseFirstCharTimedOut:
		return neterror.ErrUpstreamFirstCharTimeout
	case phaseIdleTimedOut:
		return &neterror.IdleTimeoutError{DataObserved: r.observed.Load()}
	default:
		return nil
	}
}

func (r *ReadCloser) Close() error {
	r.phase.Store(phaseClosed)
	r.timerMu.Lock()
	if r.firstTimer != nil {
		r.firstTimer.Stop()
		r.firstTimer = nil
	}
	if r.idleTimer != nil {
		r.idleTimer.Stop()
		r.idleTimer = nil
	}
	r.timerMu.Unlock()
	r.cancel(nil)
	return r.ReadCloser.Close()
}

// TimedOut reports whether a first-char or idle deadline has fired.
func (r *ReadCloser) TimedOut() bool {
	phase := r.phase.Load()
	return phase == phaseFirstCharTimedOut || phase == phaseIdleTimedOut
}
