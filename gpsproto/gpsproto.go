// Package gpsproto defines the wire format spoken between the GPS server and
// its clients (for example a media-portal PC). It is deliberately free of any
// hardware or internal dependencies so it can be imported by external programs
// that only want to consume coordinates.
//
// The protocol is JSON Lines over TCP: the server writes exactly one JSON
// object per line (terminated by '\n'), one per emitted position update. A
// client reads line by line and decodes each into a Sample. This is trivial to
// parse from any language (split on newline, JSON-decode) while staying compact
// and fast.
package gpsproto

import (
	"bufio"
	"encoding/json"
	"io"
	"time"
)

// DefaultAddr is the conventional listen/dial address for the GPS stream.
const DefaultAddr = ":9000"

// Sample is a single position update as sent on the wire. Alongside the raw
// instantaneous fix it carries the noise-averaged "best" estimate the server
// accumulates for a stationary receiver, so a client can choose whichever it
// needs without recomputing anything.
type Sample struct {
	Time     time.Time `json:"time"`      // UTC time of the fix
	Lat      float64   `json:"lat"`       // instantaneous latitude, decimal degrees
	Lon      float64   `json:"lon"`       // instantaneous longitude, decimal degrees
	Altitude float64   `json:"alt_m"`     // meters above mean sea level
	HDOP     float64   `json:"hdop"`      // horizontal dilution of precision
	Sats     int       `json:"sats"`      // satellites used in the solution
	InView   int       `json:"in_view"`   // satellites in view across constellations
	Quality  int       `json:"quality"`   // GGA fix quality (0 = no fix)
	Lock     bool      `json:"lock"`      // true when the fix is complete and valid
	SpeedKmh float64   `json:"speed_kmh"` // speed over ground, km/h (RMC)

	// Averaged best estimate over all locked samples so far (stationary use).
	AvgLat  float64 `json:"avg_lat"`
	AvgLon  float64 `json:"avg_lon"`
	SpreadM float64 `json:"spread_m"` // 1σ scatter of the averaged position, meters
	Samples int     `json:"samples"`  // number of locked fixes folded into the average
}

// QualityName maps the Sample's GGA quality code to a human-readable label,
// so consumers can log or display it without knowing the NMEA code table.
func (s Sample) QualityName() string {
	switch s.Quality {
	case 1:
		return "GPS"
	case 2:
		return "DGPS"
	case 4:
		return "RTK"
	case 5:
		return "RTK-float"
	default:
		return "no fix"
	}
}

// Encoder writes newline-delimited Samples to a stream (the server side).
type Encoder struct {
	w   *bufio.Writer
	enc *json.Encoder
}

// NewEncoder wraps w. Samples are buffered; call Flush after a write to push
// them out with minimal latency.
func NewEncoder(w io.Writer) *Encoder {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw) // json.Encoder.Encode already appends '\n'
	return &Encoder{w: bw, enc: enc}
}

// Encode writes one Sample as a single JSON line and flushes it immediately so
// the client sees fresh coordinates without waiting for the buffer to fill.
func (e *Encoder) Encode(s Sample) error {
	if err := e.enc.Encode(s); err != nil {
		return err
	}
	return e.w.Flush()
}

// Decoder reads newline-delimited Samples from a stream (the client side).
type Decoder struct{ dec *json.Decoder }

// NewDecoder wraps r. json.Decoder consumes whitespace (including the newline
// separators) between values, so successive Decode calls yield each Sample.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{dec: json.NewDecoder(bufio.NewReader(r))}
}

// Decode reads and decodes the next Sample. It returns io.EOF when the stream
// ends.
func (d *Decoder) Decode() (Sample, error) {
	var s Sample
	err := d.dec.Decode(&s)
	return s, err
}
