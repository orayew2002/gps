package nmea

import "math"

// mPerDeg is the approximate length of one degree of latitude in meters. One
// degree of longitude is this scaled by cos(latitude).
const mPerDeg = 111320.0

// Averager accumulates the coordinates of successive valid fixes and reports
// their weighted running mean. For a stationary receiver this cancels most of
// the per-sample GPS noise (typically a few meters), converging on a tighter
// estimate the longer it runs. Samples are weighted by 1/HDOP² (inverse-
// variance weighting), so well-conditioned fixes dominate poor-geometry ones
// instead of dragging the average around. The zero value is ready to use.
type Averager struct {
	n              int
	sumW           float64 // total weight
	sumLat, sumLon float64 // Σ w·coord
	sumAlt         float64
	// Weighted sums of squares, kept to report the 1σ spread of the samples.
	sumLat2, sumLon2 float64
}

// Add incorporates one fix. Fixes without a complete, valid position (no lock)
// are ignored, so only trustworthy samples move the average. Each fix is
// weighted by 1/HDOP² when HDOP is known, otherwise equally.
func (a *Averager) Add(f Fix) {
	if !f.HasLock() {
		return
	}
	w := 1.0
	if f.HDOP > 0 {
		w = 1 / (f.HDOP * f.HDOP)
	}
	a.n++
	a.sumW += w
	a.sumLat += w * f.Lat
	a.sumLon += w * f.Lon
	a.sumAlt += w * f.Altitude
	a.sumLat2 += w * f.Lat * f.Lat
	a.sumLon2 += w * f.Lon * f.Lon
}

// Count reports how many fixes have been folded into the average.
func (a *Averager) Count() int { return a.n }

// Mean returns the weighted average latitude, longitude and altitude. It
// returns zeros when no samples have been added.
func (a *Averager) Mean() (lat, lon, alt float64) {
	if a.sumW == 0 {
		return 0, 0, 0
	}
	inv := 1 / a.sumW
	return a.sumLat * inv, a.sumLon * inv, a.sumAlt * inv
}

// SpreadMeters estimates the 1σ horizontal scatter of the samples in meters — a
// rough indicator of how far the fix is still wandering. It shrinks as the
// average stabilises. Returns 0 for fewer than two samples.
func (a *Averager) SpreadMeters() float64 {
	if a.n < 2 || a.sumW == 0 {
		return 0
	}
	inv := 1 / a.sumW
	meanLat := a.sumLat * inv
	meanLon := a.sumLon * inv
	varLat := math.Max(0, a.sumLat2*inv-meanLat*meanLat)
	varLon := math.Max(0, a.sumLon2*inv-meanLon*meanLon)
	dLat := math.Sqrt(varLat) * mPerDeg
	dLon := math.Sqrt(varLon) * mPerDeg * math.Cos(meanLat*math.Pi/180)
	return math.Hypot(dLat, dLon)
}
