// Package nmea decodes the subset of NMEA-0183 sentences emitted by common
// GPS receivers (RMC, GGA, GSV) into a running positional Fix.
//
// The decoder is stateful: feed it sentences one at a time with Parser.Feed and
// read the accumulated Fix from the returned snapshot. It performs no I/O so it
// can be unit-tested and reused regardless of the underlying transport.
package nmea

import (
	"strconv"
	"strings"
	"time"
)

// Fix is a snapshot of the receiver's current navigation solution.
type Fix struct {
	Time     time.Time // UTC time of fix
	Lat, Lon float64   // decimal degrees, signed (N/E positive)
	Valid    bool      // RMC status A=valid
	Sats     int       // satellites used in solution (GGA)
	Quality  int       // GGA fix quality (0=no fix)
	InView   int       // satellites in view (GSV)

	haveTime  bool
	haveCoord bool
}

// HasLock reports whether the fix is complete and valid (time + coordinates).
func (f Fix) HasLock() bool { return f.Valid && f.haveTime && f.haveCoord }

// QualityName maps a GGA quality code to a human-readable label.
func QualityName(q int) string {
	switch q {
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

// SentenceKind classifies the most recent sentence handled by Feed, so callers
// can decide when to refresh search/lock output.
type SentenceKind int

const (
	KindOther SentenceKind = iota
	KindRMC
	KindGGA
	KindGSV
)

// Parser accumulates state across sentences into a single Fix.
type Parser struct {
	fix Fix
}

// Fix returns the current accumulated fix.
func (p *Parser) Fix() Fix { return p.fix }

// Feed validates and decodes a single raw sentence (with or without a trailing
// CR/LF). It returns the kind of sentence handled and whether it was a valid,
// recognized NMEA sentence. Invalid checksums and non-NMEA lines are ignored.
func (p *Parser) Feed(line string) (SentenceKind, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "$") || !checksumOK(line) {
		return KindOther, false
	}
	body := strings.SplitN(strings.TrimPrefix(line, "$"), "*", 2)[0]
	fields := strings.Split(body, ",")
	typ := fields[0]

	switch {
	case strings.HasSuffix(typ, "RMC"):
		parseRMC(fields, &p.fix)
		return KindRMC, true
	case strings.HasSuffix(typ, "GGA"):
		parseGGA(fields, &p.fix)
		return KindGGA, true
	case strings.HasSuffix(typ, "GSV"):
		parseGSV(fields, &p.fix)
		return KindGSV, true
	default:
		return KindOther, true
	}
}

// parseRMC: $xxRMC,hhmmss.ss,A,lat,N,lon,E,speed,course,ddmmyy,...
func parseRMC(f []string, fx *Fix) {
	if len(f) < 10 {
		return
	}
	fx.Valid = f[2] == "A"
	if t, ok := parseTimeDate(f[1], f[9]); ok {
		fx.Time = t
		fx.haveTime = true
	}
	if lat, ok := parseCoord(f[3], f[4]); ok {
		if lon, ok2 := parseCoord(f[5], f[6]); ok2 {
			fx.Lat, fx.Lon = lat, lon
			fx.haveCoord = true
		}
	}
}

// parseGGA: $xxGGA,hhmmss.ss,lat,N,lon,E,quality,sats,...
func parseGGA(f []string, fx *Fix) {
	if len(f) < 8 {
		return
	}
	fx.Quality, _ = strconv.Atoi(f[6])
	fx.Sats, _ = strconv.Atoi(f[7])
}

// parseGSV: $xxGSV,totalMsgs,msgNum,satsInView,...  (field 3 = sats in view)
func parseGSV(f []string, fx *Fix) {
	if len(f) < 4 {
		return
	}
	if n, err := strconv.Atoi(f[3]); err == nil {
		fx.InView = n
	}
}

// parseTimeDate combines NMEA time (hhmmss.ss) + date (ddmmyy) into UTC.
func parseTimeDate(hms, dmy string) (time.Time, bool) {
	if len(hms) < 6 || len(dmy) < 6 {
		return time.Time{}, false
	}
	hh, _ := strconv.Atoi(hms[0:2])
	mm, _ := strconv.Atoi(hms[2:4])
	ss, _ := strconv.Atoi(hms[4:6])
	day, _ := strconv.Atoi(dmy[0:2])
	mon, _ := strconv.Atoi(dmy[2:4])
	yr, _ := strconv.Atoi(dmy[4:6])
	return time.Date(2000+yr, time.Month(mon), day, hh, mm, ss, 0, time.UTC), true
}

// parseCoord converts NMEA ddmm.mmmm + hemisphere into signed decimal degrees.
func parseCoord(val, hemi string) (float64, bool) {
	if val == "" {
		return 0, false
	}
	dot := strings.IndexByte(val, '.')
	if dot < 3 {
		return 0, false
	}
	degEnd := dot - 2
	deg, err1 := strconv.ParseFloat(val[:degEnd], 64)
	min, err2 := strconv.ParseFloat(val[degEnd:], 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	d := deg + min/60.0
	if hemi == "S" || hemi == "W" {
		d = -d
	}
	return d, true
}

// checksumOK validates the NMEA XOR checksum after '*'.
func checksumOK(line string) bool {
	star := strings.LastIndexByte(line, '*')
	if star < 1 || star+3 > len(line) {
		return false
	}
	var cs byte
	for i := 1; i < star; i++ {
		cs ^= line[i]
	}
	want, err := strconv.ParseUint(line[star+1:star+3], 16, 8)
	if err != nil {
		return false
	}
	return byte(want) == cs
}

// LooksLikeNMEA reports whether a line is a checksum-valid NMEA sentence. Used
// by device discovery to confirm a serial port is actually a GPS receiver.
func LooksLikeNMEA(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "$") && checksumOK(line)
}
