package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	// github.com/andybalholm/brotli: the pure-Go brotli encoder the chunk
	// objects are precompressed with (ADR-0023 §3 stores chunks
	// Content-Encoding: br; the stdlib has no brotli writer). Justified in
	// chunks.go.
	"github.com/andybalholm/brotli"
)

// chunks.go — the ADR-0023 §3/§4 chunk windowing: each (region, time
// window) is ONE object, brotli-precompressed. A region's chunk list is
// CONTIGUOUS over the whole bake: every window gets a chunk even when
// every frame in it is empty (a header-only frame is 20 B), so the shim's
// "known-empty" is decided by the region set alone and a chunk-list gap is
// a manifest bug, loud here at bake time.
//
// Frames arrive strictly in index order (the re-sim is sequential). A
// region discovered mid-bake is backfilled with header-only chunks for the
// windows it missed, so all regions cover the same windows.
//
// Dependency justification (AGENTS.md "standard library first"): the
// stdlib has no brotli writer, and the chunk objects must be
// Content-Encoding: br for the static plane (ADR-0023 §3) — CDN on-the-fly
// compression and byte ranges interact badly, so precompression is part of
// the artifact contract. github.com/andybalholm/brotli is the de-facto
// pure-Go brotli (no cgo, deterministic per version); the brotli quality
// rides the bake-config digest so an encoder change lands bakes under a
// new content key.

// brotliQuality is the precompression level for chunk objects (recorded in
// the bake-config digest — a rebake at a different level lands under a new
// content key).
const brotliQuality = 9

// chunkEntry is one chunk's manifest row (index.json).
type chunkEntry struct {
	TickStart  uint64 `json:"tickStart"`
	FrameCount int    `json:"frameCount"`
	URL        string `json:"url"`
	Bytes      int    `json:"bytes"`
}

// regionChunks is one region's streaming chunk state: the open window's
// encoded frames plus the completed manifest rows.
type regionChunks struct {
	seq      int      // next chunk index to write
	winStart int      // frame index of the open window's first frame
	buf      [][]byte // encoded frames of the open window
	entries  []chunkEntry
}

// chunkSet writes one chunked stream (TSRB frames or TSRL lanes) across
// all regions.
type chunkSet struct {
	dir      string                   // absolute output dir for this stream
	urlBase  string                   // "frames" or "lanes" (manifest URL prefix)
	ext      string                   // ".tsrb.br" or ".tsrl.br"
	perChunk int                      // frames per chunk window
	tickOf   func(i int) uint64       // tick of baked frame index i
	empty    func(tick uint64) []byte // header-only frame encoding
	regions  map[string]*regionChunks
}

func newChunkSet(dir, urlBase, ext string, perChunk int, tickOf func(i int) uint64, empty func(tick uint64) []byte) *chunkSet {
	return &chunkSet{
		dir: dir, urlBase: urlBase, ext: ext, perChunk: perChunk,
		tickOf: tickOf, empty: empty,
		regions: map[string]*regionChunks{},
	}
}

// addFrame distributes one baked frame (index k): frames maps region key →
// the region's encoded frame bytes (regions absent from the map get a
// header-only frame). At a window boundary the open window is flushed for
// every region first.
func (cs *chunkSet) addFrame(k int, frames map[string][]byte) error {
	if k > 0 && k%cs.perChunk == 0 {
		for key, r := range cs.regions {
			if err := cs.closeWindow(key, r, k); err != nil {
				return err
			}
		}
	}
	w := k / cs.perChunk
	for key, fb := range frames {
		r, ok := cs.regions[key]
		if !ok {
			// New region discovered mid-bake: backfill header-only chunks
			// for the windows it missed so its chunk list is contiguous
			// from window 0.
			r = &regionChunks{winStart: w * cs.perChunk}
			cs.regions[key] = r
			for seq := 0; seq < w; seq++ {
				if err := cs.writeChunk(key, r, cs.emptyWindow(seq)); err != nil {
					return err
				}
			}
			for i := r.winStart; i < k; i++ {
				r.buf = append(r.buf, cs.empty(cs.tickOf(i)))
			}
		}
		r.buf = append(r.buf, fb)
	}
	for key, r := range cs.regions {
		if _, ok := frames[key]; !ok {
			r.buf = append(r.buf, cs.empty(cs.tickOf(k)))
		}
	}
	return nil
}

// finish flushes every region's final (possibly short) window.
// totalFrames is the baked frame count N (indices 0..N−1).
func (cs *chunkSet) finish(totalFrames int) error {
	for key, r := range cs.regions {
		for i := r.winStart + len(r.buf); i < totalFrames; i++ {
			r.buf = append(r.buf, cs.empty(cs.tickOf(i)))
		}
		if len(r.buf) == 0 {
			return fmt.Errorf("chunkset %s: region %s has an empty final window — chunk list would have a gap", cs.urlBase, key)
		}
		if err := cs.writeChunk(key, r, r.buf); err != nil {
			return err
		}
		r.buf, r.winStart = nil, r.seq*cs.perChunk
	}
	return nil
}

// closeWindow fills and flushes the open window (frames up to but not
// including k) for one region.
func (cs *chunkSet) closeWindow(key string, r *regionChunks, k int) error {
	for i := r.winStart + len(r.buf); i < k; i++ {
		r.buf = append(r.buf, cs.empty(cs.tickOf(i)))
	}
	if len(r.buf) != cs.perChunk {
		return fmt.Errorf("chunkset %s: region %s window %d closed with %d frames, want %d",
			cs.urlBase, key, r.seq, len(r.buf), cs.perChunk)
	}
	if err := cs.writeChunk(key, r, r.buf); err != nil {
		return err
	}
	r.buf, r.winStart = nil, r.seq*cs.perChunk
	return nil
}

// emptyWindow returns a full window of header-only frames (backfill).
func (cs *chunkSet) emptyWindow(seq int) [][]byte {
	out := make([][]byte, 0, cs.perChunk)
	for i := seq * cs.perChunk; i < (seq+1)*cs.perChunk; i++ {
		out = append(out, cs.empty(cs.tickOf(i)))
	}
	return out
}

// writeChunk writes one chunk object (brotli-precompressed, atomic
// temp+rename) and records its manifest row.
func (cs *chunkSet) writeChunk(region string, r *regionChunks, frames [][]byte) error {
	name := fmt.Sprintf("c%03d%s", r.seq, cs.ext)
	url := fmt.Sprintf("%s/%s/%s", cs.urlBase, regionDir(region), name)
	dir := filepath.Join(cs.dir, regionDir(region))
	var size int
	err := writeFileAtomic(dir, name, func(w io.Writer) error {
		bw := brotli.NewWriterLevel(w, brotliQuality)
		for _, f := range frames {
			if _, err := bw.Write(f); err != nil {
				return err
			}
		}
		if err := bw.Close(); err != nil {
			return err
		}
		return nil
	}, &size)
	if err != nil {
		return fmt.Errorf("write chunk %s: %w", url, err)
	}
	r.entries = append(r.entries, chunkEntry{
		TickStart:  cs.tickOf(r.seq * cs.perChunk),
		FrameCount: len(frames),
		URL:        url,
		Bytes:      size,
	})
	r.seq++
	return nil
}

// writeFileAtomic writes a file via a temp file + rename (ADR-0018's cache
// discipline: a killed bake never leaves a half-written object under its
// final name). size, when non-nil, receives the byte count written.
func writeFileAtomic(dir, name string, write func(w io.Writer) error, size *int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".tmp-"+name)
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	cw := &countingWriter{w: f}
	werr := write(cw)
	cerr := f.Close()
	if werr != nil {
		os.Remove(tmp)
		return werr
	}
	if cerr != nil {
		os.Remove(tmp)
		return cerr
	}
	if size != nil {
		*size = cw.n
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

type countingWriter struct {
	w io.Writer
	n int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}
