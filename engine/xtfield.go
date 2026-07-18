package engine

// Edie x-t observability for the M2 NGSIM credibility test: the engine-side
// consumer of the "one Edie implementation, two consumers" principle
// (docs/kb/raw/domain-trajectory-datasets/synthesis.md). The math, binning,
// CSV schema, color ramp, and wave-speed scan are ported from
// analysis/ngsim/main.go so simulated and real fields are directly
// comparable — the analysis tool is the referee.
//
// Method: Edie (1963) — for any space–time cell A, q = Σ distance / |A|,
// k = Σ time / |A|, u = q/k. Each tick, every vehicle on a measurement lane
// contributes its (distance, time) to the cell containing its segment
// midpoint, exactly like the real tool's consecutive 0.1 s sample pairs.

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"sort"
	"strconv"
)

const (
	mPerFt    = 0.3048
	ftPerMile = 5280.0
	// FpsToKmh converts ft/s to km/h (same constant as the analysis tool).
	FpsToKmh = 1.09728
)

// XTField accumulates an Edie speed field over a set of measurement lanes.
// It is an observer, not part of the world state: it reads vehicles after
// each Step and never feeds back into the sim.
type XTField struct {
	Dx, Dt float64 // cell size (ft, s)
	NX, NT int     // grid dimensions

	dist [][]float64 // veh-feet per cell
	tim  [][]float64 // veh-seconds per cell

	lanes map[string]float64 // lane ID → x offset (m) in the window coordinate
	ticks uint64             // observations so far
	last  map[uint64]xtSeg   // vehicle ID → previous observation
}

type xtSeg struct {
	tick uint64
	x    float64 // ft in the window coordinate
}

// NewXTField builds a field over lanes (lane ID → x offset in metres, see
// I80MeasLanes) with the given cell size and total span. dx/dt/span in ft
// and s — the real field's binning is 25 ft × 3 s over 1,791 ft × 900 s.
func NewXTField(lanes map[string]float64, dxFt, dtS, spanFt, spanS float64) *XTField {
	nx, nt := int(spanFt/dxFt), int(spanS/dtS)
	x := &XTField{
		Dx: dxFt, Dt: dtS, NX: nx, NT: nt,
		dist:  make([][]float64, nx),
		tim:   make([][]float64, nx),
		lanes: lanes,
		last:  make(map[uint64]xtSeg),
	}
	for i := range x.dist {
		x.dist[i] = make([]float64, nt)
		x.tim[i] = make([]float64, nt)
	}
	return x
}

// Observe folds one engine tick into the field. Call once per Step, starting
// after the warm-up; the first Observe is t = dtTick of the window.
func (x *XTField) Observe(e *Engine) {
	dt := e.Params.Dt
	x.ticks++
	j := int((float64(x.ticks) - 0.5) * dt / x.Dt)
	cur := make(map[uint64]xtSeg, len(x.last))
	for _, v := range e.Vehicles() {
		off, ok := x.lanes[v.Lane.ID]
		if !ok {
			continue
		}
		xft := (off + v.S) / mPerFt
		prev, ok := x.last[v.ID]
		cur[v.ID] = xtSeg{tick: x.ticks, x: xft}
		if !ok || prev.tick != x.ticks-1 {
			continue // entering the zone (or the network): no segment yet
		}
		i := int((prev.x + xft) / 2 / x.Dx)
		if i < 0 || i >= x.NX || j < 0 || j >= x.NT {
			continue
		}
		x.dist[i][j] += xft - prev.x
		x.tim[i][j] += dt
	}
	x.last = cur
}

// Speed returns the Edie speed field u (ft/s); cells with < 0.5 veh-s of
// presence are empty (-1), same convention as the real field.
func (x *XTField) Speed() [][]float64 {
	u := make([][]float64, x.NX)
	for i := range u {
		u[i] = make([]float64, x.NT)
		for j := range u[i] {
			if x.tim[i][j] < 0.5 {
				u[i][j] = -1
				continue
			}
			u[i][j] = x.dist[i][j] / x.tim[i][j]
		}
	}
	return u
}

// WriteCSV writes the field in the exact schema of
// data/ngsim/i80-1700-1715-field.csv: t_s,x_ft at bin centers, q in veh/h
// (all measurement lanes combined), k in veh/mi, u in ft/s; empty cells
// omitted.
func (x *XTField) WriteCSV(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	fmt.Fprintln(w, "t_s,x_ft,q_veh_per_h,k_veh_per_mi,u_ft_per_s")
	area := x.Dx * x.Dt // ft·s
	for i := 0; i < x.NX; i++ {
		for j := 0; j < x.NT; j++ {
			if x.tim[i][j] < 0.5 {
				continue
			}
			q := x.dist[i][j] / area * 3600     // veh/h
			k := x.tim[i][j] / area * ftPerMile // veh/mi
			fmt.Fprintf(w, "%.1f,%.1f,%.1f,%.1f,%.2f\n",
				(float64(j)+0.5)*x.Dt, (float64(i)+0.5)*x.Dx, q, k, x.dist[i][j]/x.tim[i][j])
		}
	}
	return w.Flush()
}

// ReadFieldCSV reads a field CSV (real or sim) back into a speed grid with
// the given binning; cells absent from the file are empty (-1). This lets
// the wave-speed measurement below run on both fields through one code path.
func ReadFieldCSV(path string, dx, dt float64) ([][]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(bufio.NewReaderSize(f, 1<<20))
	head, err := r.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[h] = i
	}
	for _, need := range []string{"t_s", "x_ft", "u_ft_per_s"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("missing column %q", need)
		}
	}
	type cell struct{ i, j int }
	var cells []cell
	var us []float64
	nx, nt := 0, 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		t, err1 := strconv.ParseFloat(rec[col["t_s"]], 64)
		x, err2 := strconv.ParseFloat(rec[col["x_ft"]], 64)
		u, err3 := strconv.ParseFloat(rec[col["u_ft_per_s"]], 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		i, j := int(x/dx), int(t/dt)
		cells = append(cells, cell{i, j})
		us = append(us, u)
		if i+1 > nx {
			nx = i + 1
		}
		if j+1 > nt {
			nt = j + 1
		}
	}
	u := make([][]float64, nx)
	for i := range u {
		u[i] = make([]float64, nt)
		for j := range u[i] {
			u[i][j] = -1
		}
	}
	for k, c := range cells {
		u[c.i][c.j] = us[k]
	}
	return u, nil
}

// WaveSpeed scans candidate speeds c (ft/s) and returns the one whose
// characteristic lines x = x0 + c·t have the lowest within-line variance of
// the speed field — congestion stripes are smooth along their own slope.
// Direct port of the analysis tool's estimator: lines seeded on the left
// edge and the top/bottom edge, only congested samples (u < threshold)
// counted, ≥10 per line and ≥500 overall.
//
// Known limitation (found in M2 validation): on fields whose congested area
// is dominated by a quasi-stationary mass (solid crawl) rather than by
// diagonal stripes, near-zero c wins — the congested-only filter lets
// vertical lines cherry-pick similar trough values. The real NGSIM field is
// stripe-dominated and the estimator reproduces −16.5 ft/s there; on
// mass-dominated sim fields use FDWaveSpeed as the robust cross-check.
func WaveSpeed(u [][]float64, dx, dt, congested float64) float64 {
	nX, nT := len(u), len(u[0])
	sampleAt := func(xf float64, j int) float64 { // bilinear in x, -1 if invalid
		fi := xf/dx - 0.5
		i0 := int(fi)
		if fi < 0 || i0 >= nX-1 {
			return -1
		}
		w := fi - float64(i0)
		va, vb := u[i0][j], u[i0+1][j]
		if va < 0 || vb < 0 {
			return -1
		}
		return va*(1-w) + vb*w
	}
	lineStats := func(x0 float64, j0 int, c float64) (sum, sum2 float64, n int) {
		for j := j0; j < nT; j++ {
			v := sampleAt(x0+c*float64(j-j0)*dt, j)
			if v < 0 || v >= congested {
				continue
			}
			sum += v
			sum2 += v * v
			n++
		}
		return
	}
	best, bestScore := 0.0, -1.0
	for c := -50.0; c <= 10.0; c += 0.5 {
		var totVar float64
		var totN int
		score := func(x0 float64, j0 int) {
			sum, sum2, n := lineStats(x0, j0, c)
			if n < 10 {
				return
			}
			mean := sum / float64(n)
			totVar += sum2 - float64(n)*mean*mean
			totN += n
		}
		for i := 0; i < nX; i++ { // left edge
			score((float64(i)+0.5)*dx, 0)
		}
		edge := float64(nX)*dx - 0.5*dx // top edge for backward waves
		if c > 0 {
			edge = 0.5 * dx // bottom edge for forward waves
		}
		for j := 1; j < nT; j++ {
			score(edge, j)
		}
		if totN < 500 {
			continue
		}
		s := totVar / float64(totN)
		if bestScore < 0 || s < bestScore {
			bestScore, best = s, c
		}
	}
	return best
}

// FDWaveSpeed estimates the congestion wave speed as the chord slope of the
// field's fundamental diagram between its two congested states (the
// shockwave definition): c = Δq/Δk between the go-state (uGoLo < u < uGoHi)
// and the jam state (u < uJam), with q and k normalized per lane. Robust on
// mass-dominated fields where WaveSpeed gets hijacked by the standing
// component. Cross-validated on the real NGSIM field (−13.7 ft/s = −15.0
// km/h vs the scan's −16.5 ft/s = −18.1 km/h). ok=false when either state
// has too few cells (< 20).
func (x *XTField) FDWaveSpeed(nLanes int, uJam, uGoLo, uGoHi float64) (ftPerS float64, ok bool) {
	area := x.Dx * x.Dt // ft·s
	var qs, ks, qg, kg float64
	var ns, ng int
	for i := 0; i < x.NX; i++ {
		for j := 0; j < x.NT; j++ {
			if x.tim[i][j] < 0.5 {
				continue
			}
			u := x.dist[i][j] / x.tim[i][j]
			q := x.dist[i][j] / area * 3600 / float64(nLanes)     // veh/h/lane
			k := x.tim[i][j] / area * ftPerMile / float64(nLanes) // veh/mi/lane
			switch {
			case u < uJam:
				qs, ks, ns = qs+q, ks+k, ns+1
			case u > uGoLo && u < uGoHi:
				qg, kg, ng = qg+q, kg+k, ng+1
			}
		}
	}
	if ns < 20 || ng < 20 {
		return 0, false
	}
	qs, ks, qg, kg = qs/float64(ns), ks/float64(ns), qg/float64(ng), kg/float64(ng)
	// c = Δq/Δk in mi/h, converted to ft/s.
	mph := (qg - qs) / (kg - ks)
	return mph * ftPerMile / 3600, true
}

// WaveStripes counts distinct stop-and-go waves that cross the measurement
// section: a wave trough appears at the downstream reference row first, then
// at the middle, then at the upstream row (backward propagation). A trough
// is a local minimum of the (smoothed) speed series below low ft/s that
// recovers by at least margin ft/s (prominence — shallow wiggles and
// solid-queue floors don't count). A chain of troughs across all three rows
// with lags in [3, 90] s counts as one crossing wave; uniform slowing yields
// at most 1. Calibrated so the real NGSIM field scores ≥ 2.
func WaveStripes(u [][]float64, dx, dt float64) int {
	return len(waveChains(u, dx, dt))
}

// waveChains finds crossing waves (see WaveStripes) and returns each chain's
// trough times at the downstream, middle, and upstream reference rows.
func waveChains(u [][]float64, dx, dt float64) [][3]float64 {
	const low, margin = 25.0, 8.0 // ft/s
	nX := len(u)
	rows := [3]int{nX / 4, nX / 2, 3 * nX / 4} // upstream → downstream
	var tr [3][]float64
	for k, i := range rows {
		tr[k] = waveTroughs(smooth(fillInvalid(u[i]), 5), dt, low, margin)
	}
	var chains [][3]float64
	i1 := 0
	for _, o3 := range tr[2] { // downstream row sees the wave first
		matched := false
		for ; i1 < len(tr[1]); i1++ {
			d := tr[1][i1] - o3
			if d > 90 {
				break
			}
			if d < 3 {
				continue
			}
			for _, o1 := range tr[0] {
				d2 := o1 - tr[1][i1]
				if d2 > 90 {
					break
				}
				if d2 >= 3 {
					chains = append(chains, [3]float64{o3, tr[1][i1], o1})
					matched = true
					break
				}
			}
			if matched {
				i1++
				break
			}
		}
	}
	return chains
}

// WaveStripeSpeeds measures the propagation speed of each crossing wave leg
// (ft/s, negative = backward): the lag of a wave's trough between adjacent
// reference rows, divided into the row spacing. Unlike the variance scan
// (WaveSpeed) and the FD chord (FDWaveSpeed), this measures each wave's
// traversal directly, so it is robust on both stripe-dominated and
// mass-dominated fields. Cross-validated on the real NGSIM field: median
// ≈ −15 ft/s ≈ −16 km/h over 5 waves (scan −16.5, FD −13.7).
func WaveStripeSpeeds(u [][]float64, dx, dt float64) []float64 {
	nX := len(u)
	rows := [3]int{nX / 4, nX / 2, 3 * nX / 4}
	gap1 := float64(rows[1]-rows[0]) * dx
	gap2 := float64(rows[2]-rows[1]) * dx
	var speeds []float64
	for _, ch := range waveChains(u, dx, dt) {
		speeds = append(speeds, -gap1/(ch[1]-ch[0]), -gap2/(ch[2]-ch[1]))
	}
	return speeds
}

// MedianSpeeds returns the median of a per-wave speed list (0 when empty).
// Used so acceptance reads a typical wave, not a best-case one.
func MedianSpeeds(speeds []float64) float64 {
	if len(speeds) == 0 {
		return 0
	}
	s := make([]float64, len(speeds))
	copy(s, speeds)
	sort.Float64s(s)
	return s[len(s)/2]
}

// waveTroughs returns the times (s) of prominent congested troughs in a speed
// series: a candidate starts when the series drops below low, tracks the
// running minimum, and is accepted when the series recovers by margin above
// the minimum; a trailing unrecovered dip at the window end also counts.
func waveTroughs(s []float64, dt, low, margin float64) []float64 {
	var times []float64
	in := false
	cand, candJ := 0.0, 0
	for j, v := range s {
		if !in {
			if v >= 0 && v < low {
				in, cand, candJ = true, v, j
			}
			continue
		}
		if v < cand {
			cand, candJ = v, j
		}
		if v > cand+margin {
			times = append(times, float64(candJ)*dt)
			in = false
			if v < low { // still congested: next trough starts immediately
				in, cand, candJ = true, v, j
			}
		}
	}
	if in {
		times = append(times, float64(candJ)*dt)
	}
	return times
}

// fillInvalid replaces empty (-1) cells by the nearest valid value (forward
// then backward fill) so smoothing/episodes work on a complete series.
func fillInvalid(s []float64) []float64 {
	out := make([]float64, len(s))
	last := -1.0
	for j, v := range s {
		if v >= 0 {
			last = v
		}
		out[j] = last
	}
	next := -1.0
	for j := len(s) - 1; j >= 0; j-- {
		if s[j] >= 0 {
			next = s[j]
		}
		if out[j] < 0 {
			out[j] = next
		}
	}
	return out
}

// smooth applies a width-w boxcar (truncated at the edges).
func smooth(s []float64, w int) []float64 {
	out := make([]float64, len(s))
	for j := range s {
		a, b := j-w/2, j+w/2
		if a < 0 {
			a = 0
		}
		if b >= len(s) {
			b = len(s) - 1
		}
		sum := 0.0
		for k := a; k <= b; k++ {
			sum += s[k]
		}
		out[j] = sum / float64(b-a+1)
	}
	return out
}

// Sequential blue ramp (light=fast … dark=stopped), same palette as the real
// heatmap. Empty cells are white.
var xtRamp = []color.NRGBA{
	{0xcd, 0xe2, 0xfb, 0xff}, {0xb7, 0xd3, 0xf6, 0xff}, {0x9e, 0xc5, 0xf4, 0xff},
	{0x86, 0xb6, 0xef, 0xff}, {0x6d, 0xa7, 0xec, 0xff}, {0x55, 0x98, 0xe7, 0xff},
	{0x39, 0x87, 0xe5, 0xff}, {0x2a, 0x78, 0xd6, 0xff}, {0x25, 0x6a, 0xbf, 0xff},
	{0x1c, 0x5c, 0xab, 0xff}, {0x18, 0x4f, 0x95, 0xff}, {0x10, 0x42, 0x81, 0xff},
	{0x0d, 0x36, 0x6b, 0xff},
}

func xtSpeedColor(v, vmax float64) color.NRGBA {
	if v < 0 {
		return color.NRGBA{0xff, 0xff, 0xff, 0xff} // no data
	}
	f := v / vmax
	if f > 1 {
		f = 1
	}
	pos := (1 - f) * float64(len(xtRamp)-1) // slow → end of ramp (dark)
	i := int(pos)
	if i >= len(xtRamp)-1 {
		return xtRamp[len(xtRamp)-1]
	}
	w := pos - float64(i)
	a, b := xtRamp[i], xtRamp[i+1]
	lerp := func(x, y uint8) uint8 { return uint8(float64(x)*(1-w) + float64(y)*w + 0.5) }
	return color.NRGBA{lerp(a.R, b.R), lerp(a.G, b.G), lerp(a.B, b.B), 0xff}
}

// WritePNG renders a speed field as a heatmap with the real tool's
// conventions: x up (direction of travel), t right, dark blue = congested.
func WritePNG(path string, u [][]float64, vmax float64, cw, ch int) error {
	nX, nT := len(u), len(u[0])
	img := image.NewNRGBA(image.Rect(0, 0, nT*cw, nX*ch))
	for i := 0; i < nX; i++ {
		for j := 0; j < nT; j++ {
			c := xtSpeedColor(u[i][j], vmax)
			// row 0 of the image = top = max y (direction of travel points up)
			py0 := (nX - 1 - i) * ch
			for py := py0; py < py0+ch; py++ {
				for px := j * cw; px < (j+1)*cw; px++ {
					img.SetNRGBA(px, py, c)
				}
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
