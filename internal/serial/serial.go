// Package serial discovers and opens the serial port a GPS receiver is exposed
// on. USB GPS units (e.g. u-blox CDC-ACM) enumerate as /dev/ttyACM* or
// /dev/ttyUSB*, but the exact node is not stable across reboots or replugs, so
// the port is found by probing each candidate for live NMEA data.
package serial

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/orayew2002/gps/internal/nmea"
)

// candidateGlobs returns the device-node patterns scanned during discovery, in
// priority order, for the OS the binary is running on. USB GPS units enumerate
// under different names per platform:
//
//   - Linux:  /dev/ttyACM* (CDC-ACM, e.g. u-blox) then /dev/ttyUSB* (USB-serial)
//   - macOS:  /dev/cu.usbmodem* (CDC-ACM) then /dev/cu.usbserial* (USB-serial);
//     the cu.* call-out nodes are used (not tty.*) so opening does not block on
//     carrier detect. Bluetooth cu.* devices are intentionally excluded.
//
// On any other OS we try the union so the service degrades gracefully.
func candidateGlobs() []string {
	switch runtime.GOOS {
	case "linux":
		return []string{"/dev/ttyACM*", "/dev/ttyUSB*"}
	case "darwin":
		return []string{"/dev/cu.usbmodem*", "/dev/cu.usbserial*"}
	default:
		return []string{
			"/dev/ttyACM*", "/dev/ttyUSB*",
			"/dev/cu.usbmodem*", "/dev/cu.usbserial*",
		}
	}
}

// Port is an opened serial connection to a GPS receiver.
type Port struct {
	*os.File
	Path string
}

// Open opens a specific device path without probing. Use this when the caller
// already knows the port (e.g. an explicit -port flag).
func Open(path string) (*Port, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &Port{File: f, Path: path}, nil
}

// Candidates returns the sorted set of device nodes that could be a GPS.
func Candidates() []string {
	var out []string
	for _, g := range candidateGlobs() {
		matches, _ := filepath.Glob(g)
		out = append(out, matches...)
	}
	sort.Strings(out)
	return out
}

// Discover scans the candidate device nodes and returns the first one that
// emits a valid NMEA sentence within probeTimeout. It returns an opened Port
// ready to read from. ErrNotFound is returned when no GPS is present.
//
// The context aborts the scan early (e.g. on shutdown).
func Discover(ctx context.Context, probeTimeout time.Duration) (*Port, error) {
	for _, path := range Candidates() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p, err := Open(path)
		if err != nil {
			continue // busy or permission denied — skip
		}
		if probe(p, probeTimeout) {
			return p, nil
		}
		p.Close()
	}
	return nil, ErrNotFound
}

// ErrNotFound indicates no GPS device was detected among the candidates.
var ErrNotFound = fmt.Errorf("no GPS device found")

// probe reads from an open port and reports whether it produces a valid NMEA
// sentence before the timeout. On timeout it closes the port to unblock the
// background read; callers must not reuse a port that failed to probe.
func probe(p *Port, timeout time.Duration) bool {
	found := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(p)
		for sc.Scan() {
			if nmea.LooksLikeNMEA(sc.Text()) {
				found <- true
				return
			}
		}
		found <- false
	}()

	select {
	case ok := <-found:
		return ok
	case <-time.After(timeout):
		p.Close() // unblock the blocked Read in the goroutine
		return false
	}
}
