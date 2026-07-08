// Package gpsrun lets another Go program embed the whole GPS service —
// discover the USB receiver, parse NMEA, keep the freshest Sample — without
// running gps-service as a separate process. A host application (for example
// media-admin) starts Runner.Run in a goroutine and reads Latest whenever it
// needs the current position; Events reports real modem lifecycle (device
// connected, unplugged, still searching).
//
// Optionally, setting Config.ServeAddr also re-streams samples over TCP in the
// same JSON-Lines protocol the standalone gps-service speaks, so existing
// network clients keep working while the host program owns the receiver.
package gpsrun

import (
	"context"
	"log"
	"time"

	"github.com/orayew2002/gps/gpsproto"
	"github.com/orayew2002/gps/internal/live"
	"github.com/orayew2002/gps/internal/server"
	"github.com/orayew2002/gps/internal/service"
)

// Config controls the embedded receiver. The zero value auto-detects the
// device and does not serve TCP.
type Config struct {
	// Port pins a specific serial device (e.g. /dev/ttyACM0). Empty means
	// auto-detect among /dev/ttyACM* and /dev/ttyUSB* by probing for NMEA.
	Port string
	// ProbeTimeout bounds how long each candidate device is sniffed during
	// auto-detection. Zero uses the service default (3s).
	ProbeTimeout time.Duration
	// ServeAddr, if non-empty (e.g. ":9000"), additionally streams samples
	// over TCP for external clients, exactly like `gps-service -serve`.
	ServeAddr string
	// ServeRate is the per-client send interval in serve mode. Zero means
	// 100ms (10 Hz).
	ServeRate time.Duration
	// Logger receives TCP server logs in serve mode. Nil uses the std logger.
	Logger *log.Logger
}

// Events reports receiver lifecycle to the host program. Any field may be nil.
// Callbacks run on the read goroutine; keep them cheap.
type Events struct {
	// OnConnect fires when the serial device is opened and confirmed
	// streaming NMEA (the modem is physically present and talking).
	OnConnect func(device string)
	// OnDisconnect fires when the device stops (unplug, read error).
	OnDisconnect func(device string, err error)
	// OnWaiting fires each time a discovery pass finds no device.
	OnWaiting func()
	// OnSample fires for every published position sample.
	OnSample func(gpsproto.Sample)
}

// Runner is an embedded GPS service instance.
type Runner struct {
	tr  *live.Tracker
	cfg Config
}

// New builds a Runner. Call Run to start it.
func New(cfg Config, ev Events) *Runner {
	scfg := service.DefaultConfig()
	if cfg.Port != "" {
		scfg.Port = cfg.Port
	}
	if cfg.ProbeTimeout > 0 {
		scfg.ProbeTimeout = cfg.ProbeTimeout
	}

	tr := live.NewWithHandler(scfg, service.Handler{
		OnConnect:    ev.OnConnect,
		OnDisconnect: ev.OnDisconnect,
		OnWaiting:    ev.OnWaiting,
	})
	tr.OnSample = ev.OnSample

	return &Runner{tr: tr, cfg: cfg}
}

// Latest returns the most recent sample and true, or a zero Sample and false
// if no fix has been produced yet. Safe for concurrent use; never blocks.
func (r *Runner) Latest() (gpsproto.Sample, bool) {
	return r.tr.Latest()
}

// Run drives the receiver (and the TCP stream server when configured) until
// ctx is cancelled. It blocks; run it in its own goroutine. It returns nil on
// clean shutdown.
func (r *Runner) Run(ctx context.Context) error {
	if r.cfg.ServeAddr == "" {
		return r.tr.Run(ctx)
	}

	// Serve mode: tracker and TCP server run side by side; the first error
	// (or clean ctx shutdown) wins and cancels the other.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)
	go func() { errCh <- r.tr.Run(ctx) }()
	go func() {
		srv := server.New(r.tr, r.cfg.ServeRate, r.cfg.Logger)
		errCh <- srv.ListenAndServe(ctx, r.cfg.ServeAddr)
	}()

	err := <-errCh
	cancel()
	<-errCh
	return err
}
