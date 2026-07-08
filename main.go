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

	"github.com/orayew2002/gps/internal/live"
	"github.com/orayew2002/gps/internal/nmea"
	"github.com/orayew2002/gps/internal/server"
	"github.com/orayew2002/gps/internal/service"
)

// version is overridden at build time via -ldflags "-X main.version=...".
// See the Makefile.
var version = "dev"

func main() {
	cfg := service.DefaultConfig()
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.StringVar(&cfg.Port, "port", "", "GPS serial device (default: auto-detect)")
	flag.BoolVar(&cfg.StopOnFirstLock, "once", false, "exit after first valid fix")
	flag.DurationVar(&cfg.ProbeTimeout, "probe-timeout", cfg.ProbeTimeout, "per-device NMEA probe timeout")
	serveAddr := flag.String("serve", "", "stream fixes over TCP on this address (e.g. :9000)")
	serveRate := flag.Duration("rate", 100*time.Millisecond, "per-client send interval in -serve mode")
	flag.Parse()

	if *showVersion {
		fmt.Printf("gps-service %s\n", version)
		return
	}

	// Cancel on SIGINT/SIGTERM for clean shutdown (and to unblock a stuck read).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := log.New(os.Stderr, "", log.LstdFlags)

	// Server mode: read the receiver in a background goroutine and stream the
	// live position to TCP clients (e.g. the media-portal PC). The tracker keeps
	// the newest fix in a lock-free slot so serving is instant.
	if *serveAddr != "" {
		tr := live.New(cfg)
		go func() {
			if err := tr.Run(ctx); err != nil {
				logger.Printf("gps tracker stopped: %v", err)
			}
		}()
		srv := server.New(tr, *serveRate, logger)
		if err := srv.ListenAndServe(ctx, *serveAddr); err != nil {
			logger.Fatalf("gps-service: %v", err)
		}
		return
	}
	// avg accumulates every locked fix so we can report a noise-averaged
	// position that is far tighter than any single sample. It is read again
	// after Run returns to print the final best estimate on shutdown.
	var avg nmea.Averager
	svc := service.New(cfg, newConsoleHandler(logger, &avg))

	if err := svc.Run(ctx); err != nil {
		logger.Fatalf("gps-service: %v", err)
	}

	// On clean shutdown, print the averaged "best" coordinate and a map link.
	if n := avg.Count(); n > 0 {
		lat, lon, alt := avg.Mean()
		const bar = "────────────────────────────────────────────────────"
		fmt.Printf("\n\n%s\n", bar)
		fmt.Printf("  📍 BEST FIX   averaged over %d samples · ±%.1fm\n", n, avg.SpreadMeters())
		fmt.Printf("     lat %.6f   lon %.6f   alt %.0fm\n", lat, lon, alt)
		fmt.Printf("     🔗 %s\n", yandexURL(lat, lon))
		fmt.Printf("%s\n", bar)
	}
}

// yandexURL builds a Yandex.Maps link centered on the coordinate with a pin.
// Yandex expects longitude,latitude order (the reverse of how fixes read).
func yandexURL(lat, lon float64) string {
	return fmt.Sprintf("https://yandex.com/maps/?ll=%.6f,%.6f&z=17&pt=%.6f,%.6f", lon, lat, lon, lat)
}

// newConsoleHandler renders lifecycle events and fixes to the terminal. Fix and
// search status share a single rewriting line (\r); lifecycle messages go to
// the logger so they remain visible in the scrollback.
func newConsoleHandler(logger *log.Logger, avg *nmea.Averager) service.Handler {
	var lastWaiting time.Time
	var lastLinkAt int // sample count at which we last logged a map link
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
				// Fold this sample into the running average and lead with the
				// averaged position (the best estimate); the instantaneous fix
				// and ±spread ride alongside so convergence stays visible.
				avg.Add(fix)
				n := avg.Count()
				aLat, aLon, _ := avg.Mean()
				fmt.Printf("\r🛰️  LOCK   %.6f, %.6f   ±%.1fm  ·  n=%d  ·  sat %d/%d  ·  HDOP %.1f  ·  %s        ",
					aLat, aLon, avg.SpreadMeters(), n,
					fix.Sats, fix.InView, fix.HDOP, nmea.QualityName(fix.Quality))
				// Every 20 fixes, park a clickable map link on its own line in the
				// scrollback (leading \n lifts off the live line, trailing \n frees
				// it again) so a recent averaged position is always at hand.
				if n-lastLinkAt >= 20 {
					lastLinkAt = n
					fmt.Printf("\n   🔗 n=%d  ±%.1fm   %s\n", n, avg.SpreadMeters(), yandexURL(aLat, aLon))
				}
			case kind == nmea.KindGSV || kind == nmea.KindGGA:
				fmt.Printf("\r🔍 SEARCH  in view %2d  ·  used %d  ·  %-7s ·  (need clear sky)        ",
					fix.InView, fix.Sats, nmea.QualityName(fix.Quality))
			}
		},
	}
}
