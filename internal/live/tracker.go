// Package live turns the supervised GPS service into a shared, always-current
// source of coordinates. It runs the receiver in a background goroutine and
// publishes the newest fix into a lock-free slot, so any number of consumers
// (e.g. socket clients) can read the latest position without ever blocking on
// serial I/O. This is what makes coordinate reads "instant": a read is a single
// atomic pointer load, never a device round-trip.
package live

import (
	"context"
	"sync/atomic"

	"github.com/orayew2002/gps/gpsproto"
	"github.com/orayew2002/gps/internal/nmea"
	"github.com/orayew2002/gps/internal/service"
)

// Tracker owns the running GPS service and the most recent Sample.
type Tracker struct {
	svc    *service.Service
	latest atomic.Pointer[gpsproto.Sample] // newest published sample (nil until first)
	avg    nmea.Averager                   // noise-averaging accumulator (read loop only)

	// OnSample, if set before Run, is called with every published sample on
	// the read goroutine. Keep it cheap.
	OnSample func(gpsproto.Sample)
}

// New builds a Tracker over the given service config. Pass service.DefaultConfig()
// for auto-detection, optionally setting Port to pin a device.
func New(cfg service.Config) *Tracker {
	return NewWithHandler(cfg, service.Handler{})
}

// NewWithHandler builds a Tracker that also forwards lifecycle events
// (OnConnect/OnDisconnect/OnWaiting, and OnSentence after the sample is
// published) to h, so an embedding program can observe device state while the
// tracker keeps maintaining Latest.
func NewWithHandler(cfg service.Config, h service.Handler) *Tracker {
	t := &Tracker{}
	t.svc = service.New(cfg, service.Handler{
		OnConnect:    h.OnConnect,
		OnDisconnect: h.OnDisconnect,
		OnWaiting:    h.OnWaiting,
		OnSentence: func(fix nmea.Fix, kind nmea.SentenceKind) {
			t.onSentence(fix, kind)
			if h.OnSentence != nil {
				h.OnSentence(fix, kind)
			}
		},
	})
	return t
}

// Run drives the GPS receiver until ctx is cancelled. Call it in its own
// goroutine; it blocks for the lifetime of the service and reconnects across
// unplug/replug on its own.
func (t *Tracker) Run(ctx context.Context) error {
	return t.svc.Run(ctx)
}

// Latest returns the most recent sample and true, or a zero Sample and false if
// no fix has been produced yet. It is safe to call concurrently from any
// goroutine and never blocks.
func (t *Tracker) Latest() (gpsproto.Sample, bool) {
	if s := t.latest.Load(); s != nil {
		return *s, true
	}
	return gpsproto.Sample{}, false
}

// onSentence runs on the service read loop for every recognized sentence. It
// folds locked fixes into the running average and republishes the latest
// sample. It is the only writer of t.latest and the only user of t.avg, so no
// locking is needed here.
func (t *Tracker) onSentence(fix nmea.Fix, _ nmea.SentenceKind) {
	if fix.HasLock() {
		t.avg.Add(fix)
	}
	aLat, aLon, _ := t.avg.Mean()
	s := gpsproto.Sample{
		Time:     fix.Time,
		Lat:      fix.Lat,
		Lon:      fix.Lon,
		Altitude: fix.Altitude,
		HDOP:     fix.HDOP,
		Sats:     fix.Sats,
		InView:   fix.InView,
		Quality:  fix.Quality,
		Lock:     fix.HasLock(),
		SpeedKmh: fix.SpeedKmh,
		AvgLat:   aLat,
		AvgLon:   aLon,
		SpreadM:  t.avg.SpreadMeters(),
		Samples:  t.avg.Count(),
	}
	t.latest.Store(&s)
	if t.OnSample != nil {
		t.OnSample(s)
	}
}
