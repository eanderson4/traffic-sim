// Command ngsim-xt computes Edie's generalized traffic variables (flow q,
// density k, speed u) on an x–t grid from an NGSIM trajectory CSV (as produced
// by download-i80.sh), renders the speed field as a heatmap PNG, and estimates
// the dominant congestion-wave speed.
//
// Method: Edie (1963) — for any space–time region A, q = Σ distance / |A|,
// k = Σ time / |A|, u = q/k. Each consecutive 0.1 s sample pair of a vehicle
// contributes its (distance, time) to the grid cell containing the segment
// midpoint; at NGSIM resolution a vehicle moves ≲10 ft per frame, well under
// the cell size, so midpoint assignment ≈ exact area splitting.
//
// Wave speed: congestion structures are smooth along their characteristic
// (the insight behind Treiber & Helbing's Adaptive Smoothing Method). We scan
// candidate wave speeds c and measure the mean squared change of the speed
// field along lines x = x0 + c·Δt, restricted to congested cells; the c that
// minimizes it is the dominant wave speed (expected ≈ −15…−20 km/h).
//
// This is the seed of the engine's observability layer: the same Edie
// computation must later consume simulated trajectories over NATS.
package main

import (
	"bufio"
	"encoding/csv"
	"flag"
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
	ftPerMile = 5280.0
	fpsToKmh  = 1.09728 // ft/s → km/h
	fpsToMph  = 0.681818
)

type sample struct {
	t float64 // seconds since window start
	y float64 // feet along section
}

func main() {
	in := flag.String("in", "", "input CSV from download-i80.sh (required)")
	outPNG := flag.String("png", "xt-heatmap.png", "output heatmap PNG")
	outCSV := flag.String("field", "", "optional output CSV of the (t,x,q,k,u) field")
	dt := flag.Float64("dt", 3.0, "time bin size (s)")
	dx := flag.Float64("dx", 25.0, "space bin size (ft)")
	laneMin := flag.Int("lane-min", 1, "min lane_id to include")
	laneMax := flag.Int("lane-max", 6, "max lane_id to include (I-80: 1=HOV … 6 rightmost; 7 = on-ramp)")
	vmax := flag.Float64("vmax", 60, "speed (ft/s) mapped to the lightest color")
	congested := flag.Float64("congested", 25, "cells below this speed (ft/s) drive the wave-speed fit")
	cellW := flag.Int("cell-w", 4, "pixels per time bin")
	cellH := flag.Int("cell-h", 6, "pixels per space bin")
	flag.Parse()
	if *in == "" {
		flag.Usage()
		os.Exit(2)
	}

	trajs, t0, yMax, err := load(*in, *laneMin, *laneMax)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(1)
	}
	nVeh := len(trajs)

	// Grid: rows = space bins (index 0 at y=0), cols = time bins.
	// Window length comes from the data itself.
	tMax := 0.0
	for _, tr := range trajs {
		if last := tr[len(tr)-1].t; last > tMax {
			tMax = last
		}
	}
	nT := int(tMax / *dt)
	nX := int(yMax / *dx)
	if nT == 0 || nX == 0 {
		fmt.Fprintln(os.Stderr, "degenerate grid")
		os.Exit(1)
	}
	dist := make([][]float64, nX) // veh-feet per cell
	tim := make([][]float64, nX)  // veh-seconds per cell
	for i := range dist {
		dist[i] = make([]float64, nT)
		tim[i] = make([]float64, nT)
	}

	// Edie accumulation over consecutive sample pairs.
	pairs := 0
	for _, tr := range trajs {
		for k := 1; k < len(tr); k++ {
			a, b := tr[k-1], tr[k]
			gap := b.t - a.t
			if gap <= 0 || gap > 0.15 { // tracking dropout — not a traversal
				continue
			}
			tm := (a.t + b.t) / 2
			ym := (a.y + b.y) / 2
			i, j := int(ym / *dx), int(tm / *dt)
			if i < 0 || i >= nX || j < 0 || j >= nT {
				continue
			}
			dist[i][j] += b.y - a.y
			tim[i][j] += gap
			pairs++
		}
	}

	// Speed field; cells with too little occupancy stay empty (NaN-like: u<0).
	area := *dx * *dt // ft·s
	u := make([][]float64, nX)
	for i := range u {
		u[i] = make([]float64, nT)
		for j := range u[i] {
			if tim[i][j] < 0.5 { // < 0.5 veh-s of presence: unreliable
				u[i][j] = -1
				continue
			}
			u[i][j] = dist[i][j] / tim[i][j]
		}
	}

	wave := waveSpeed(u, *dx, *dt, *congested)

	if err := writePNG(*outPNG, u, *vmax, *cellW, *cellH); err != nil {
		fmt.Fprintln(os.Stderr, "png:", err)
		os.Exit(1)
	}
	if *outCSV != "" {
		if err := writeField(*outCSV, dist, tim, u, area, *dx, *dt); err != nil {
			fmt.Fprintln(os.Stderr, "field:", err)
			os.Exit(1)
		}
	}

	fmt.Printf("vehicles: %d   sample pairs: %d   window: %.0f s   section: %.0f ft   grid: %d×%d (dx=%.0f ft, dt=%.0f s)\n",
		nVeh, pairs, tMax, yMax, nX, nT, *dx, *dt)
	fmt.Printf("t0 epoch ms: %d\n", t0)
	fmt.Printf("dominant congestion wave speed: %.1f ft/s = %.1f km/h = %.1f mph\n",
		wave, wave*fpsToKmh, wave*fpsToMph)
	fmt.Printf("heatmap: %s (x up = direction of travel, t right; dark = congested)\n", *outPNG)
}

// load parses the CSV and returns per-vehicle time-sorted trajectories,
// the epoch-ms origin, and the max local_y seen.
func load(path string, laneMin, laneMax int) (map[int][]sample, int64, float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	r := csv.NewReader(bufio.NewReaderSize(f, 1<<20))
	r.ReuseRecord = true

	head, err := r.Read()
	if err != nil {
		return nil, 0, 0, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[h] = i
	}
	for _, need := range []string{"vehicle_id", "global_time", "local_y", "lane_id"} {
		if _, ok := col[need]; !ok {
			return nil, 0, 0, fmt.Errorf("missing column %q", need)
		}
	}

	type raw struct {
		tms int64
		y   float64
	}
	byVeh := map[int][]raw{}
	var tmin int64 = 1<<63 - 1
	yMax := 0.0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, 0, err
		}
		lane, err := strconv.Atoi(rec[col["lane_id"]])
		if err != nil || lane < laneMin || lane > laneMax {
			continue
		}
		veh, err1 := strconv.Atoi(rec[col["vehicle_id"]])
		tms, err2 := strconv.ParseInt(rec[col["global_time"]], 10, 64)
		y, err3 := strconv.ParseFloat(rec[col["local_y"]], 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		byVeh[veh] = append(byVeh[veh], raw{tms, y})
		if tms < tmin {
			tmin = tms
		}
		if y > yMax {
			yMax = y
		}
	}

	trajs := make(map[int][]sample, len(byVeh))
	for veh, rs := range byVeh {
		sort.Slice(rs, func(i, j int) bool { return rs[i].tms < rs[j].tms })
		tr := make([]sample, len(rs))
		for i, s := range rs {
			tr[i] = sample{t: float64(s.tms-tmin) / 1000, y: s.y}
		}
		trajs[veh] = tr
	}
	return trajs, tmin, yMax, nil
}

// waveSpeed scans candidate speeds c (ft/s) and returns the one whose
// characteristic lines x = x0 + c·t have the lowest within-line variance of
// the speed field — congestion stripes are smooth along their own slope, so
// the aligned c minimizes it. Lines are seeded on the left edge and on the
// top/bottom edge (depending on sign of c) so slanted lines cover the whole
// window. Only congested samples (u < threshold) enter the variance, and a
// line needs ≥10 of them to count.
func waveSpeed(u [][]float64, dx, dt, congested float64) float64 {
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

// Sequential blue ramp (light=fast … dark=stopped), steps 100→700.
var ramp = []color.NRGBA{
	{0xcd, 0xe2, 0xfb, 0xff}, {0xb7, 0xd3, 0xf6, 0xff}, {0x9e, 0xc5, 0xf4, 0xff},
	{0x86, 0xb6, 0xef, 0xff}, {0x6d, 0xa7, 0xec, 0xff}, {0x55, 0x98, 0xe7, 0xff},
	{0x39, 0x87, 0xe5, 0xff}, {0x2a, 0x78, 0xd6, 0xff}, {0x25, 0x6a, 0xbf, 0xff},
	{0x1c, 0x5c, 0xab, 0xff}, {0x18, 0x4f, 0x95, 0xff}, {0x10, 0x42, 0x81, 0xff},
	{0x0d, 0x36, 0x6b, 0xff},
}

func speedColor(v, vmax float64) color.NRGBA {
	if v < 0 {
		return color.NRGBA{0xff, 0xff, 0xff, 0xff} // no data
	}
	f := v / vmax
	if f > 1 {
		f = 1
	}
	pos := (1 - f) * float64(len(ramp)-1) // slow → end of ramp (dark)
	i := int(pos)
	if i >= len(ramp)-1 {
		return ramp[len(ramp)-1]
	}
	w := pos - float64(i)
	a, b := ramp[i], ramp[i+1]
	lerp := func(x, y uint8) uint8 { return uint8(float64(x)*(1-w) + float64(y)*w + 0.5) }
	return color.NRGBA{lerp(a.R, b.R), lerp(a.G, b.G), lerp(a.B, b.B), 0xff}
}

func writePNG(path string, u [][]float64, vmax float64, cw, ch int) error {
	nX, nT := len(u), len(u[0])
	img := image.NewNRGBA(image.Rect(0, 0, nT*cw, nX*ch))
	for i := 0; i < nX; i++ {
		for j := 0; j < nT; j++ {
			c := speedColor(u[i][j], vmax)
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

func writeField(path string, dist, tim, u [][]float64, area, dx, dt float64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	fmt.Fprintln(w, "t_s,x_ft,q_veh_per_h,k_veh_per_mi,u_ft_per_s")
	for i := range u {
		for j := range u[i] {
			if u[i][j] < 0 {
				continue
			}
			q := dist[i][j] / area * 3600     // veh/h (all included lanes combined)
			k := tim[i][j] / area * ftPerMile // veh/mi (all included lanes combined)
			fmt.Fprintf(w, "%.1f,%.1f,%.1f,%.1f,%.2f\n",
				(float64(j)+0.5)*dt, (float64(i)+0.5)*dx, q, k, u[i][j])
		}
	}
	return w.Flush()
}
