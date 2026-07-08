package nmea

import (
	"math"
	"testing"
	"time"
)

// approx compares two coordinates within ~1 cm of tolerance.
func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestChecksumOK(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"valid RMC", "$GPRMC,123519,A,4807.038,N,01131.000,E,022.4,084.4,230624,003.1,W*64", true},
		{"bad checksum", "$GPRMC,123519,A,4807.038,N,01131.000,E,022.4,084.4,230624,003.1,W*00", false},
		{"no star", "$GPRMC,123519,A,4807.038,N", false},
		{"empty", "", false},
		{"not nmea", "hello world", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checksumOK(tt.line); got != tt.want {
				t.Errorf("checksumOK(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestLooksLikeNMEA(t *testing.T) {
	if !LooksLikeNMEA("  $GPGGA,000000.00,,,,,0,00,99.99,,,,,,*66\r\n") {
		t.Error("expected trimmed valid sentence to be recognized")
	}
	if LooksLikeNMEA("$GPGGA,broken") {
		t.Error("expected sentence without checksum to be rejected")
	}
}

func TestParseRMC_FullLock(t *testing.T) {
	var p Parser
	// 48°07.038'N, 11°31.000'E on 1994-03-23 12:35:19 UTC.
	kind, ok := p.Feed("$GPRMC,123519,A,4807.038,N,01131.000,E,022.4,084.4,230624,003.1,W*64")
	if !ok || kind != KindRMC {
		t.Fatalf("Feed RMC: ok=%v kind=%v", ok, kind)
	}
	fix := p.Fix()
	if !fix.Valid {
		t.Error("expected Valid=true for status A")
	}
	wantTime := time.Date(2024, 6, 23, 12, 35, 19, 0, time.UTC)
	if !fix.Time.Equal(wantTime) {
		t.Errorf("time = %v, want %v", fix.Time, wantTime)
	}
	if !approx(fix.Lat, 48+7.038/60) {
		t.Errorf("lat = %f", fix.Lat)
	}
	if !approx(fix.Lon, 11+31.000/60) {
		t.Errorf("lon = %f", fix.Lon)
	}
	if !approx(fix.SpeedKmh, 22.4*1.852) {
		t.Errorf("speed = %f, want %f", fix.SpeedKmh, 22.4*1.852)
	}
}

func TestParseCoord_Hemispheres(t *testing.T) {
	tests := []struct {
		val, hemi string
		want      float64
	}{
		{"4807.038", "N", 48 + 7.038/60},
		{"4807.038", "S", -(48 + 7.038/60)},
		{"01131.000", "E", 11 + 31.000/60},
		{"01131.000", "W", -(11 + 31.000/60)},
	}
	for _, tt := range tests {
		got, ok := parseCoord(tt.val, tt.hemi)
		if !ok || !approx(got, tt.want) {
			t.Errorf("parseCoord(%q,%q) = %f,%v want %f", tt.val, tt.hemi, got, ok, tt.want)
		}
	}
	if _, ok := parseCoord("", "N"); ok {
		t.Error("empty coordinate should not be ok")
	}
}

func TestParseGGA_QualityAndSats(t *testing.T) {
	var p Parser
	p.Feed("$GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,*47")
	fix := p.Fix()
	if fix.Quality != 1 {
		t.Errorf("quality = %d, want 1", fix.Quality)
	}
	if fix.Sats != 8 {
		t.Errorf("sats = %d, want 8", fix.Sats)
	}
	if !approx(fix.HDOP, 0.9) {
		t.Errorf("HDOP = %f, want 0.9", fix.HDOP)
	}
	if !approx(fix.Altitude, 545.4) {
		t.Errorf("altitude = %f, want 545.4", fix.Altitude)
	}
}

// TestGSV_SumsAcrossConstellations is the regression test for the bug where a
// multi-GNSS receiver's GLONASS/Galileo counts overwrote the GPS count instead
// of adding to it. The total in view must be the sum across talkers.
func TestGSV_SumsAcrossConstellations(t *testing.T) {
	var p Parser
	p.Feed("$GPGSV,1,1,07,01,40,083,42*46") // GPS reports 7 in view
	if got := p.Fix().InView; got != 7 {
		t.Fatalf("after GPGSV InView = %d, want 7", got)
	}
	p.Feed("$GLGSV,1,1,05,65,30,045,30*52") // GLONASS reports 5 in view
	if got := p.Fix().InView; got != 12 {
		t.Errorf("after GLGSV InView = %d, want 12 (7 GPS + 5 GLONASS)", got)
	}
	// A fresh GPGSV updates GPS without dropping GLONASS.
	p.Feed("$GPGSV,1,1,09,01,40,083,42*48")
	if got := p.Fix().InView; got != 14 {
		t.Errorf("after refreshed GPGSV InView = %d, want 14 (9 GPS + 5 GLONASS)", got)
	}
}

func TestHasLock_RequiresAllParts(t *testing.T) {
	var p Parser
	if p.Fix().HasLock() {
		t.Fatal("fresh parser should not report lock")
	}
	// RMC alone supplies validity, time and coordinates → lock.
	p.Feed("$GPRMC,123519,A,4807.038,N,01131.000,E,022.4,084.4,230394,003.1,W*6A")
	if !p.Fix().HasLock() {
		t.Error("expected lock after a valid RMC")
	}
}

func TestFeed_IgnoresGarbage(t *testing.T) {
	var p Parser
	if kind, ok := p.Feed("random noise"); ok || kind != KindOther {
		t.Errorf("garbage line accepted: kind=%v ok=%v", kind, ok)
	}
}

func TestAverager_MeanAndSpread(t *testing.T) {
	var a Averager
	if a.Count() != 0 {
		t.Fatalf("fresh averager count = %d, want 0", a.Count())
	}
	// A fix without lock must be ignored.
	a.Add(Fix{Lat: 10, Lon: 20})
	if a.Count() != 0 {
		t.Fatalf("non-lock fix was averaged: count = %d", a.Count())
	}

	// Two locked samples straddling a center point: the mean lands in between.
	locked := func(lat, lon, alt float64) Fix {
		return Fix{Lat: lat, Lon: lon, Altitude: alt, Valid: true, haveTime: true, haveCoord: true}
	}
	a.Add(locked(37.903538, 58.343001, 200))
	a.Add(locked(37.903529, 58.343033, 210))
	if a.Count() != 2 {
		t.Fatalf("count = %d, want 2", a.Count())
	}
	lat, lon, alt := a.Mean()
	if !approx(lat, (37.903538+37.903529)/2) || !approx(lon, (58.343001+58.343033)/2) {
		t.Errorf("mean = %f,%f", lat, lon)
	}
	if !approx(alt, 205) {
		t.Errorf("mean alt = %f, want 205", alt)
	}
	// Samples ~3 m apart → spread on the order of a couple of meters, not zero.
	if s := a.SpreadMeters(); s <= 0 || s > 10 {
		t.Errorf("spread = %f m, want (0,10]", s)
	}
}

func TestAverager_EmptyMean(t *testing.T) {
	var a Averager
	if lat, lon, alt := a.Mean(); lat != 0 || lon != 0 || alt != 0 {
		t.Errorf("empty mean = %f,%f,%f, want zeros", lat, lon, alt)
	}
	if s := a.SpreadMeters(); s != 0 {
		t.Errorf("empty spread = %f, want 0", s)
	}
}

func TestQualityName(t *testing.T) {
	cases := map[int]string{0: "no fix", 1: "GPS", 2: "DGPS", 4: "RTK", 5: "RTK-float", 99: "no fix"}
	for q, want := range cases {
		if got := QualityName(q); got != want {
			t.Errorf("QualityName(%d) = %q, want %q", q, got, want)
		}
	}
}
