// Package client connects to a GPS stream server and delivers coordinates to
// the host program. It is meant to be imported by a consumer such as a
// media-portal PC and run in its own goroutine: Stream dials the server, keeps
// the newest Sample available for instant reads, and transparently reconnects
// if the link drops.
package client

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/orayew2002/gps/gpsproto"
)

// Client holds the latest coordinates received from a GPS stream server.
type Client struct {
	latest atomic.Pointer[gpsproto.Sample]
	// OnSample, if set, is called for every received sample in addition to
	// updating Latest. It runs on the read goroutine, so keep it cheap.
	OnSample func(gpsproto.Sample)
}

// New returns an empty Client. Set OnSample before calling Stream if you want a
// push callback as well as the Latest snapshot.
func New() *Client { return &Client{} }

// Latest returns the most recent sample and true, or a zero Sample and false if
// nothing has arrived yet. Safe for concurrent use; never blocks.
func (c *Client) Latest() (gpsproto.Sample, bool) {
	if s := c.latest.Load(); s != nil {
		return *s, true
	}
	return gpsproto.Sample{}, false
}

// Stream connects to addr (e.g. "192.168.1.50:9000") and reads samples until
// ctx is cancelled. It reconnects automatically after any error with a short
// backoff, so a single call keeps coordinates flowing across server restarts
// and network blips. It returns only when ctx is cancelled.
//
// Typical use from the media portal:
//
//	c := client.New()
//	go c.Stream(ctx, "192.168.1.50:9000")
//	// ...elsewhere, whenever you need the position:
//	if s, ok := c.Latest(); ok {
//	        useCoordinates(s.Lat, s.Lon)
//	}
func (c *Client) Stream(ctx context.Context, addr string) error {
	const backoff = time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := c.readOnce(ctx, addr); err != nil {
			// Connection failed or dropped — wait, then retry.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
}

// readOnce dials once and pumps samples into the latest slot until the stream
// ends or ctx is cancelled.
func (c *Client) readOnce(ctx context.Context, addr string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Closing the connection on cancellation unblocks a stuck Decode.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	dec := gpsproto.NewDecoder(conn)
	for {
		s, err := dec.Decode()
		if err != nil {
			return err
		}
		c.latest.Store(&s)
		if c.OnSample != nil {
			c.OnSample(s)
		}
	}
}
