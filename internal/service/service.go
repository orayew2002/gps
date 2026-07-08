// Package service supervises a GPS receiver: it discovers the device, streams
// NMEA sentences into a parser, and transparently recovers from unplug/replug
// events with capped exponential backoff. It owns the lifecycle so callers only
// need to react to fixes via Handler.
package service

import (
	"bufio"
	"context"
	"errors"
	"time"

	"gps/internal/nmea"
	"gps/internal/serial"
)

// Handler receives lifecycle and data events from the running service.
// Implementations must be cheap; they are called from the read loop. Any field
// may be nil.
type Handler struct {
	// OnConnect fires when a device is opened and confirmed streaming.
	OnConnect func(path string)
	// OnDisconnect fires when the active device stops (unplug, read error).
	OnDisconnect func(path string, err error)
	// OnWaiting fires each time discovery runs and finds no device.
	OnWaiting func()
	// OnSentence fires after every recognized NMEA sentence with the current fix.
	OnSentence func(fix nmea.Fix, kind nmea.SentenceKind)
}

// Config controls discovery and reconnection behavior. The zero value is not
// usable; use DefaultConfig and override as needed.
type Config struct {
	// Port, if set, pins the service to a specific device node and disables
	// discovery probing. Reconnection still applies.
	Port string
	// ProbeTimeout bounds how long each candidate is sniffed for NMEA data.
	ProbeTimeout time.Duration
	// MinBackoff / MaxBackoff bound the reconnect/rediscover delay.
	MinBackoff, MaxBackoff time.Duration
	// StopOnFirstLock ends the service after the first complete valid fix.
	StopOnFirstLock bool
}

// DefaultConfig returns sane defaults for an always-on GPS service.
func DefaultConfig() Config {
	return Config{
		ProbeTimeout: 3 * time.Second,
		MinBackoff:   500 * time.Millisecond,
		MaxBackoff:   10 * time.Second,
	}
}

// Service streams fixes from a GPS receiver, recovering across disconnects.
type Service struct {
	cfg Config
	h   Handler
}

// New builds a Service from config and a handler.
func New(cfg Config, h Handler) *Service {
	return &Service{cfg: cfg, h: h}
}

// errLock is the sentinel used internally to unwind the read loop after a
// requested stop-on-first-lock.
var errLock = errors.New("first lock reached")

// Run blocks until ctx is cancelled, supervising the device across its full
// connect/disconnect lifecycle. It returns nil on clean shutdown (ctx cancelled
// or first lock when configured), or a non-nil error only for unrecoverable
// conditions.
func (s *Service) Run(ctx context.Context) error {
	backoff := s.cfg.MinBackoff
	for {
		if ctx.Err() != nil {
			return nil
		}

		port, err := s.acquire(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			// No device yet — notify, wait, retry. Discovery itself is the
			// "new connection" detector: a freshly plugged device shows up on
			// the next pass.
			if s.h.OnWaiting != nil {
				s.h.OnWaiting()
			}
			if !sleep(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff, s.cfg.MaxBackoff)
			continue
		}

		backoff = s.cfg.MinBackoff // healthy connection — reset backoff
		if s.h.OnConnect != nil {
			s.h.OnConnect(port.Path)
		}

		err = s.stream(ctx, port)
		port.Close()

		switch {
		case errors.Is(err, errLock):
			return nil
		case ctx.Err() != nil:
			return nil
		default:
			// Disconnect / read error: report and loop back to re-acquire,
			// which transparently handles replug onto a different node.
			if s.h.OnDisconnect != nil {
				s.h.OnDisconnect(port.Path, err)
			}
		}
	}
}

// acquire opens the configured port, or discovers one when no port is pinned.
func (s *Service) acquire(ctx context.Context) (*serial.Port, error) {
	if s.cfg.Port != "" {
		return serial.Open(s.cfg.Port)
	}
	return serial.Discover(ctx, s.cfg.ProbeTimeout)
}

// stream reads NMEA sentences until the device stops, ctx is cancelled, or the
// configured first-lock condition is met.
func (s *Service) stream(ctx context.Context, port *serial.Port) error {
	// Cancellation unblocks a stuck Read by closing the port.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			port.Close()
		case <-done:
		}
	}()

	var p nmea.Parser
	sc := bufio.NewScanner(port)
	for sc.Scan() {
		kind, ok := p.Feed(sc.Text())
		if !ok {
			continue
		}
		fix := p.Fix()
		if s.h.OnSentence != nil {
			s.h.OnSentence(fix, kind)
		}
		if s.cfg.StopOnFirstLock && fix.HasLock() {
			return errLock
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Scanner stopped: EOF or read error both mean the device went away.
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("device closed (EOF)")
}

// nextBackoff doubles d up to the cap.
func nextBackoff(d, max time.Duration) time.Duration {
	d *= 2
	if d > max {
		return max
	}
	return d
}

// sleep waits for d or ctx cancellation; it returns false if ctx was cancelled.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
