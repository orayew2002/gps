// Command gps-service reads NMEA sentences from a USB GPS receiver (e.g. a
// u-blox CDC-ACM unit) and prints the current UTC time and coordinates.
//
// The service auto-detects the device among /dev/ttyACM* and /dev/ttyUSB* by
// probing for live NMEA data, so no port needs to be configured. It keeps
// running across unplug/replug events, automatically reconnecting to whichever
// node the receiver re-enumerates on. Pass -port to pin a specific device.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gps-service/internal/nmea"
	"gps-service/internal/service"
)

func main() {
	cfg := service.DefaultConfig()
	flag.StringVar(&cfg.Port, "port", "", "GPS serial device (default: auto-detect)")
	flag.BoolVar(&cfg.StopOnFirstLock, "once", false, "exit after first valid fix")
	flag.DurationVar(&cfg.ProbeTimeout, "probe-timeout", cfg.ProbeTimeout, "per-device NMEA probe timeout")
	flag.Parse()

	// Cancel on SIGINT/SIGTERM for clean shutdown (and to unblock a stuck read).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := log.New(os.Stderr, "", log.LstdFlags)
	svc := service.New(cfg, newConsoleHandler(logger))

	if err := svc.Run(ctx); err != nil {
		logger.Fatalf("gps-service: %v", err)
	}
}

// newConsoleHandler renders lifecycle events and fixes to the terminal. Fix and
// search status share a single rewriting line (\r); lifecycle messages go to
// the logger so they remain visible in the scrollback.
func newConsoleHandler(logger *log.Logger) service.Handler {
	var lastWaiting time.Time
	return service.Handler{
		OnConnect: func(path string) {
			fmt.Println()
			logger.Printf("connected: %s — waiting for fix (needs clear sky view)...", path)
		},
		OnDisconnect: func(path string, err error) {
			fmt.Println()
			logger.Printf("disconnected: %s (%v) — searching for device...", path, err)
		},
		OnWaiting: func() {
			// Throttle the "no device" notice so it does not spam the log.
			if time.Since(lastWaiting) > 5*time.Second {
				logger.Printf("no GPS device detected — will keep scanning...")
				lastWaiting = time.Now()
			}
		},
		OnSentence: func(fix nmea.Fix, kind nmea.SentenceKind) {
			switch {
			case fix.HasLock():
				fmt.Printf("\rLOCK  UTC %s  lat %.6f  lon %.6f  sats %d  (%s)        ",
					fix.Time.Format("2006-01-02 15:04:05"),
					fix.Lat, fix.Lon, fix.Sats, nmea.QualityName(fix.Quality))
			case kind == nmea.KindGSV || kind == nmea.KindGGA:
				fmt.Printf("\rSEARCH  sats in view %2d  used %d  fix: %-9s  (need clear sky)   ",
					fix.InView, fix.Sats, nmea.QualityName(fix.Quality))
			}
		},
	}
}
