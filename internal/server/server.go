// Package server exposes a live GPS Tracker over TCP as a JSON-Lines stream.
// Each accepted connection receives the newest Sample at a fixed rate for as
// long as it stays connected, so a media-portal PC (or any client) gets a
// steady, low-latency feed of coordinates without polling the hardware itself.
package server

import (
	"context"
	"log"
	"net"
	"time"

	"gps/gpsproto"
	"gps/internal/live"
)

// Server streams a Tracker's latest sample to every TCP client.
type Server struct {
	tr     *live.Tracker
	rate   time.Duration // interval between samples sent to each client
	logger *log.Logger
}

// New builds a Server that serves from tr. rate is the per-client send interval
// (e.g. 100ms for 10 Hz); values <= 0 default to 100ms. logger may be nil.
func New(tr *live.Tracker, rate time.Duration, logger *log.Logger) *Server {
	if rate <= 0 {
		rate = 100 * time.Millisecond
	}
	if logger == nil {
		logger = log.New(log.Writer(), "", log.LstdFlags)
	}
	return &Server{tr: tr, rate: rate, logger: logger}
}

// ListenAndServe binds addr (e.g. ":9000") and serves clients until ctx is
// cancelled, at which point the listener is closed and the call returns.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	s.logger.Printf("gps stream listening on %s (%.0f Hz)", addr, float64(time.Second)/float64(s.rate))

	// Close the listener when ctx is cancelled to unblock Accept.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return err
		}
		go s.serve(ctx, conn)
	}
}

// serve pushes the latest sample to one client every s.rate until the client
// disconnects or the server shuts down. Sends are skipped until the first fix
// exists, so a client never receives a meaningless all-zero position.
func (s *Server) serve(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	s.logger.Printf("client connected: %s", conn.RemoteAddr())
	defer func() { s.logger.Printf("client disconnected: %s", conn.RemoteAddr()) }()

	enc := gpsproto.NewEncoder(conn)
	ticker := time.NewTicker(s.rate)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sample, ok := s.tr.Latest()
			if !ok {
				continue // no fix yet — nothing to send
			}
			// A short write deadline keeps a stalled client from blocking its
			// own goroutine forever; any write error ends the connection.
			conn.SetWriteDeadline(time.Now().Add(s.rate + time.Second))
			if err := enc.Encode(sample); err != nil {
				return
			}
		}
	}
}
