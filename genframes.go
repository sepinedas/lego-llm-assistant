package main

// genframes.go — Procedurally generates all PNG animation frames.
//
// Frame layout on disk (created under framesDir):
//
//   frames/
//     left/
//       neutral/  00.png … NN.png
//       speaking/ 00.png … NN.png
//       asleep/   00.png … NN.png
//     right/      (mirrored horizontally)
//       neutral/  …
//       speaking/ …
//       asleep/   …

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// ────────────────────────────────────────────────────────────────────────────
// Colour palette
// ────────────────────────────────────────────────────────────────────────────

var (
	colBlack     = color.RGBA{0, 0, 0, 255}
	colWhite     = color.RGBA{255, 255, 255, 255}
	colIris      = color.RGBA{30, 160, 200, 255} // teal-blue
	colIrisDark  = color.RGBA{15, 80, 110, 255}  // limbus ring
	colPupil     = color.RGBA{10, 10, 15, 255}
	colHighlight = color.RGBA{240, 248, 255, 255}
	colSkin      = color.RGBA{8, 8, 8, 255} // eyelid (near-black)
)

// ────────────────────────────────────────────────────────────────────────────
// Primitive drawing helpers
// ────────────────────────────────────────────────────────────────────────────

// fillCircle paints every pixel within radius r of (cx,cy) with c.
func fillCircle(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	r2 := r * r
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= r2 {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

// drawAA draws an antialiased filled circle using sub-pixel coverage.
func drawAA(img *image.RGBA, cx, cy float64, r float64, c color.RGBA) {
	rint := int(r) + 2
	cxI, cyI := int(math.Round(cx)), int(math.Round(cy))
	for y := cyI - rint; y <= cyI+rint; y++ {
		for x := cxI - rint; x <= cxI+rint; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			dist := math.Sqrt(dx*dx+dy*dy) - r
			alpha := math.Max(0, math.Min(1, 0.5-dist))
			if alpha <= 0 {
				continue
			}
			px := img.RGBAAt(x, y)
			a := float64(c.A) / 255 * alpha
			blendRGBA(img, x, y, px, c, a)
		}
	}
}

func blendRGBA(img *image.RGBA, x, y int, bg, fg color.RGBA, fgA float64) {
	ba := 1 - fgA
	img.SetRGBA(x, y, color.RGBA{
		R: uint8(float64(bg.R)*ba + float64(fg.R)*fgA),
		G: uint8(float64(bg.G)*ba + float64(fg.G)*fgA),
		B: uint8(float64(bg.B)*ba + float64(fg.B)*fgA),
		A: 255,
	})
}

// drawEyelid fills the upper portion of the sclera circle with the lid colour.
// lidFrac 0.0 = barely visible, 1.0 = fully closed.
func drawEyelid(img *image.RGBA, cx, cy, scleraR int, lidFrac float64, c color.RGBA) {
	if lidFrac <= 0 {
		return
	}
	// The eyelid descends from the top of the sclera circle.
	lidBottom := cy - scleraR + int(math.Round(float64(2*scleraR)*lidFrac))
	scleraR2 := scleraR * scleraR
	for y := cy - scleraR; y <= lidBottom; y++ {
		for x := cx - scleraR; x <= cx+scleraR; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= scleraR2 {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

// drawLowerLid fills the lower portion (for a sleepy droop from below).
func drawLowerLid(img *image.RGBA, cx, cy, scleraR int, lidFrac float64, c color.RGBA) {
	if lidFrac <= 0 {
		return
	}
	lidTop := cy + scleraR - int(math.Round(float64(2*scleraR)*lidFrac))
	scleraR2 := scleraR * scleraR
	for y := lidTop; y <= cy+scleraR; y++ {
		for x := cx - scleraR; x <= cx+scleraR; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= scleraR2 {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// High-level eye drawing
// ────────────────────────────────────────────────────────────────────────────

// eyeParams fully describes one eye frame.
type eyeParams struct {
	// openFrac: how open the upper lid is (1.0 = fully open, 0.0 = fully closed).
	openFrac float64
	// lowerLidFrac: how far the lower lid intrudes upward (0 = none).
	lowerLidFrac float64
	// irisOffX, irisOffY: iris centre offset from display centre in pixels.
	irisOffX, irisOffY float64
	// irisSqueeze: vertical scale of iris (1.0 normal, <1 squint).
	irisSqueeze float64
	// pupilScale: pupil radius as fraction of iris radius.
	pupilScale float64
}

const (
	cx         = DisplayW / 2
	cy         = DisplayH / 2
	scleraR    = 105
	irisR      = 50
	pupilR     = 26 // base; scaled by pupilScale
	highlightR = 9
)

func drawEye(p eyeParams) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, DisplayW, DisplayH))
	draw.Draw(img, img.Bounds(), &image.Uniform{colBlack}, image.Point{}, draw.Src)

	// ── Sclera (white of the eye) ────────────────────────────────────────────
	fillCircle(img, cx, cy, scleraR, colWhite)

	// ── Iris ─────────────────────────────────────────────────────────────────
	// Apply squeeze as a vertical ellipse by drawing horizontal slices.
	icx := float64(cx) + p.irisOffX
	icy := float64(cy) + p.irisOffY
	irisRf := float64(irisR)
	irisRy := irisRf * p.irisSqueeze // vertical radius

	// Limbus ring (darker border around iris).
	for y := int(icy) - irisR - 2; y <= int(icy)+irisR+2; y++ {
		for x := int(icx) - irisR - 2; x <= int(icx)+irisR+2; x++ {
			dx := float64(x) - icx
			dy := float64(y) - icy
			if dx*dx/(irisRf+2)/(irisRf+2)+dy*dy/(irisRy+2)/(irisRy+2) <= 1 {
				img.SetRGBA(x, y, colIrisDark)
			}
		}
	}
	// Iris fill.
	for y := int(icy) - irisR; y <= int(icy)+irisR; y++ {
		for x := int(icx) - irisR; x <= int(icx)+irisR; x++ {
			dx := float64(x) - icx
			dy := float64(y) - icy
			if dx*dx/(irisRf*irisRf)+dy*dy/(irisRy*irisRy) <= 1 {
				img.SetRGBA(x, y, colIris)
			}
		}
	}

	// ── Pupil ────────────────────────────────────────────────────────────────
	pr := int(math.Round(float64(pupilR) * p.pupilScale))
	fillCircle(img, int(math.Round(icx)), int(math.Round(icy)), pr, colPupil)

	// ── Specular highlight ───────────────────────────────────────────────────
	hx := int(math.Round(icx + float64(irisR)*0.35))
	hy := int(math.Round(icy - float64(irisR)*0.4))
	fillCircle(img, hx, hy, highlightR, colHighlight)
	// Smaller secondary highlight.
	fillCircle(img, hx+5, hy+8, highlightR/2, colHighlight)

	// ── Eyelids ───────────────────────────────────────────────────────────────
	upperFrac := 1.0 - p.openFrac
	drawEyelid(img, cx, cy, scleraR, upperFrac, colSkin)
	drawLowerLid(img, cx, cy, scleraR, p.lowerLidFrac, colSkin)

	return img
}

// mirrorH returns a horizontally flipped copy of src.
func mirrorH(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.SetRGBA(b.Max.X-1-x+b.Min.X, y, src.RGBAAt(x, y))
		}
	}
	return dst
}

// ────────────────────────────────────────────────────────────────────────────
// Animation frame sequences
// ────────────────────────────────────────────────────────────────────────────

// neutralFrames returns a blink cycle for the neutral expression.
// Frames 0-11 = open; 12 = half-close; 13 = closed; 14 = half-open; 15 = open.
func neutralFrames() []eyeParams {
	open := eyeParams{openFrac: 1.0, lowerLidFrac: 0.04, irisOffX: 0, irisOffY: 0, irisSqueeze: 1.0, pupilScale: 1.0}
	halfC := eyeParams{openFrac: 0.55, lowerLidFrac: 0.04, irisOffX: 0, irisOffY: 0, irisSqueeze: 0.7, pupilScale: 1.0}
	closed := eyeParams{openFrac: 0.0, lowerLidFrac: 0.04, irisOffX: 0, irisOffY: 0, irisSqueeze: 0.1, pupilScale: 0.6}
	frames := make([]eyeParams, 16)
	for i := 0; i < 12; i++ {
		frames[i] = open
	}
	frames[12] = halfC
	frames[13] = closed
	frames[14] = halfC
	frames[15] = open
	return frames
}

// speakingFrames returns an eye that darts and squints slightly to mimic speech.
func speakingFrames() []eyeParams {
	frames := make([]eyeParams, 12)
	// Subtle iris sway left→centre→right with a gentle squint.
	offsets := [][2]float64{
		{0, -3},
		{-5, -2},
		{-8, 0},
		{-5, 2},
		{0, 3},
		{5, 2},
		{8, 0},
		{5, -2},
		{0, -3},
		{-4, 0},
		{0, 0},
		{4, 0},
	}
	for i, o := range offsets {
		sq := 0.88 + 0.06*math.Sin(float64(i)*math.Pi/6)
		frames[i] = eyeParams{
			openFrac:     0.88,
			lowerLidFrac: 0.06,
			irisOffX:     o[0],
			irisOffY:     o[1],
			irisSqueeze:  sq,
			pupilScale:   1.0,
		}
	}
	return frames
}

// asleepFrames returns a slow-close then sleepy-flutter sequence.
// Phase 1 (0-11): gradual close. Phase 2 (12-19): gentle flutter while droopy.
func asleepFrames() []eyeParams {
	frames := make([]eyeParams, 20)
	// Gradual close.
	for i := 0; i < 12; i++ {
		t := float64(i) / 11.0
		// Ease in (quadratic).
		tf := t * t
		frames[i] = eyeParams{
			openFrac:     1.0 - tf*0.75,
			lowerLidFrac: tf * 0.15,
			irisOffX:     0,
			irisOffY:     tf * 10, // iris drifts down as eye closes
			irisSqueeze:  1.0 - tf*0.5,
			pupilScale:   1.0 - tf*0.3,
		}
	}
	// Sleepy flutter (slightly open, then shut again).
	flutterOpen := []float64{0.25, 0.30, 0.18, 0.25, 0.15, 0.22, 0.12, 0.20}
	for i, v := range flutterOpen {
		frames[12+i] = eyeParams{
			openFrac:     v,
			lowerLidFrac: 0.15,
			irisOffX:     0,
			irisOffY:     12,
			irisSqueeze:  0.45,
			pupilScale:   0.7,
		}
	}
	return frames
}

// ────────────────────────────────────────────────────────────────────────────
// File I/O
// ────────────────────────────────────────────────────────────────────────────

func saveFrames(dir string, frames []eyeParams, mirror bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for i, p := range frames {
		img := drawEye(p)
		var out image.Image = img
		if mirror {
			// Mirror iris offset for right eye.
			pm := p
			pm.irisOffX = -p.irisOffX
			out = mirrorH(drawEye(pm))
		}
		path := filepath.Join(dir, fmt.Sprintf("%02d.png", i))
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		if err := png.Encode(f, out); err != nil {
			f.Close()
			return err
		}
		f.Close()
	}
	return nil
}

// generateFrames creates all PNG frames under framesDir.
// left eye is canonical, right eye is horizontally mirrored.
func generateFrames(framesDir string) error {
	type anim struct {
		name   string
		frames []eyeParams
	}
	anims := []anim{
		{"neutral", neutralFrames()},
		{"speaking", speakingFrames()},
		{"asleep", asleepFrames()},
	}
	for _, a := range anims {
		leftDir := filepath.Join(framesDir, "left", a.name)
		rightDir := filepath.Join(framesDir, "right", a.name)
		if err := saveFrames(leftDir, a.frames, false); err != nil {
			return fmt.Errorf("left %s: %w", a.name, err)
		}
		if err := saveFrames(rightDir, a.frames, true); err != nil {
			return fmt.Errorf("right %s: %w", a.name, err)
		}
		fmt.Printf("  generated %d frames → %s/{left,right}/%s/\n", len(a.frames), framesDir, a.name)
	}
	return nil
}
