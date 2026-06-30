// gps-service reads NMEA sentences from a u-blox GPS receiver exposed as a
// USB CDC-ACM serial device and prints the current UTC time and coordinates.
//
// The u-blox 7 enumerates as /dev/ttyACM0 (USB CDC), so the port can be opened
// as a plain file — no termios/baud configuration is required.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

type fix struct {
	t         time.Time // UTC time of fix
	lat, lon  float64   // decimal degrees
	valid     bool      // RMC status A=valid
	sats      int       // satellites used (from GGA)
	quality   int       // GGA fix quality (0=no fix)
	inView    int       // satellites in view (from GSV)
	haveTime  bool
	haveCoord bool
}

var fixQuality = map[int]string{0: "no fix", 1: "GPS", 2: "DGPS", 4: "RTK", 5: "RTK-float"}

func main() {
	port := flag.String("port", "/dev/ttyACM0", "GPS serial device")
	once := flag.Bool("once", false, "exit after first valid fix")
	flag.Parse()

	f, err := os.Open(*port)
	if err != nil {
		log.Fatalf("open %s: %v", *port, err)
	}
	defer f.Close()

	log.Printf("reading %s — waiting for fix (needs clear sky view)...", *port)

	var cur fix
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "$") || !checksumOK(line) {
			continue
		}
		body := strings.SplitN(strings.TrimPrefix(line, "$"), "*", 2)[0]
		fields := strings.Split(body, ",")
		typ := fields[0]
		switch {
		case strings.HasSuffix(typ, "RMC"):
			parseRMC(fields, &cur)
		case strings.HasSuffix(typ, "GGA"):
			parseGGA(fields, &cur)
		case strings.HasSuffix(typ, "GSV"):
			parseGSV(fields, &cur)
		default:
			continue
		}

		if cur.valid && cur.haveTime && cur.haveCoord {
			fmt.Printf("\rLOCK  UTC %s  lat %.6f  lon %.6f  sats %d  (%s)        ",
				cur.t.Format("2006-01-02 15:04:05"), cur.lat, cur.lon, cur.sats, fixQuality[cur.quality])
			if *once {
				fmt.Println()
				return
			}
		} else if typ != "" && (strings.HasSuffix(typ, "GSV") || strings.HasSuffix(typ, "GGA")) {
			// no lock yet — show search progress so user sees it working
			fmt.Printf("\rSEARCH  sats in view %2d  used %d  fix: %-9s  (need clear sky)   ",
				cur.inView, cur.sats, fixQuality[cur.quality])
		}
	}
	if err := sc.Err(); err != nil {
		log.Fatalf("read: %v", err)
	}
}

// parseRMC: $xxRMC,hhmmss.ss,A,lat,N,lon,E,speed,course,ddmmyy,...
func parseRMC(f []string, fx *fix) {
	if len(f) < 10 {
		return
	}
	fx.valid = f[2] == "A"
	if t, ok := parseTimeDate(f[1], f[9]); ok {
		fx.t = t
		fx.haveTime = true
	}
	if lat, ok := parseCoord(f[3], f[4]); ok {
		if lon, ok2 := parseCoord(f[5], f[6]); ok2 {
			fx.lat, fx.lon = lat, lon
			fx.haveCoord = true
		}
	}
}

// parseGGA: $xxGGA,hhmmss.ss,lat,N,lon,E,quality,sats,...
func parseGGA(f []string, fx *fix) {
	if len(f) < 8 {
		return
	}
	fx.quality, _ = strconv.Atoi(f[6])
	fx.sats, _ = strconv.Atoi(f[7])
}

// parseGSV: $xxGSV,totalMsgs,msgNum,satsInView,...  (field 3 = sats in view)
func parseGSV(f []string, fx *fix) {
	if len(f) < 4 {
		return
	}
	if n, err := strconv.Atoi(f[3]); err == nil {
		fx.inView = n
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
