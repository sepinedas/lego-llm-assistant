package main

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"math/rand"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/spi"
)

// ── Display constants ─────────────────────────────────────────────────────────

const (
	W = 240
	H = 240
)

// ── GC9A01 low-level driver ───────────────────────────────────────────────────

type GC9A01 struct {
	conn spi.Conn
	dc   gpio.PinOut
	rst  gpio.PinOut
}

func newGC9A01(conn spi.Conn, dc, rst gpio.PinOut) (*GC9A01, error) {
	d := &GC9A01{conn: conn, dc: dc, rst: rst}
	if err := d.hardReset(); err != nil {
		return nil, err
	}
	if err := d.init(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *GC9A01) hardReset() error {
	if err := d.rst.Out(gpio.High); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	if err := d.rst.Out(gpio.Low); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	if err := d.rst.Out(gpio.High); err != nil {
		return err
	}
	time.Sleep(120 * time.Millisecond)
	return nil
}

func (d *GC9A01) writeCmd(cmd byte) error {
	if err := d.dc.Out(gpio.Low); err != nil {
		return err
	}
	return d.conn.Tx([]byte{cmd}, nil)
}

func (d *GC9A01) writeData(data ...byte) error {
	if err := d.dc.Out(gpio.High); err != nil {
		return err
	}
	return d.conn.Tx(data, nil)
}

// init sends the GC9A01 initialisation sequence (datasheet + common community sequence).
func (d *GC9A01) init() error {
	type step struct {
		cmd  byte
		data []byte
		wait time.Duration
	}
	seq := []step{
		{0xEF, nil, 0},
		{0xEB, []byte{0x14}, 0},
		{0xFE, nil, 0},
		{0xEF, nil, 0},
		{0xEB, []byte{0x14}, 0},
		{0x84, []byte{0x40}, 0},
		{0x85, []byte{0xFF}, 0},
		{0x86, []byte{0xFF}, 0},
		{0x87, []byte{0xFF}, 0},
		{0x88, []byte{0x0A}, 0},
		{0x89, []byte{0x21}, 0},
		{0x8A, []byte{0x00}, 0},
		{0x8B, []byte{0x80}, 0},
		{0x8C, []byte{0x01}, 0},
		{0x8D, []byte{0x01}, 0},
		{0x8E, []byte{0xFF}, 0},
		{0x8F, []byte{0xFF}, 0},
		{0xB6, []byte{0x00, 0x20}, 0},
		// MADCTL: row/col order, BGR
		{0x36, []byte{0x08}, 0},
		// COLMOD: 16-bit RGB565
		{0x3A, []byte{0x05}, 0},
		{0x90, []byte{0x08, 0x08, 0x08, 0x08}, 0},
		{0xBD, []byte{0x06}, 0},
		{0xBC, []byte{0x00}, 0},
		{0xFF, []byte{0x60, 0x01, 0x04}, 0},
		{0xC3, []byte{0x13}, 0},
		{0xC4, []byte{0x13}, 0},
		{0xC9, []byte{0x22}, 0},
		{0xBE, []byte{0x11}, 0},
		{0xE1, []byte{0x10, 0x0E}, 0},
		{0xDF, []byte{0x21, 0x0C, 0x02}, 0},
		{0xF0, []byte{0x45, 0x09, 0x08, 0x08, 0x26, 0x2A}, 0},
		{0xF1, []byte{0x43, 0x70, 0x72, 0x36, 0x37, 0x6F}, 0},
		{0xF2, []byte{0x45, 0x09, 0x08, 0x08, 0x26, 0x2A}, 0},
		{0xF3, []byte{0x43, 0x70, 0x72, 0x36, 0x37, 0x6F}, 0},
		{0xED, []byte{0x1B, 0x0B}, 0},
		{0xAE, []byte{0x77}, 0},
		{0xCD, []byte{0x63}, 0},
		{0x70, []byte{0x07, 0x07, 0x04, 0x0E, 0x0F, 0x09, 0x07, 0x08, 0x03}, 0},
		{0xE8, []byte{0x34}, 0},
		{0x62, []byte{0x18, 0x0D, 0x71, 0xED, 0x70, 0x70, 0x18, 0x0F, 0x71, 0xEF, 0x70, 0x70}, 0},
		{0x63, []byte{0x18, 0x11, 0x71, 0xF1, 0x70, 0x70, 0x18, 0x13, 0x71, 0xF3, 0x70, 0x70}, 0},
		{0x64, []byte{0x28, 0x29, 0xF1, 0x01, 0xF1, 0x00, 0x07}, 0},
		{0x66, []byte{0x3C, 0x00, 0xCD, 0x67, 0x45, 0x45, 0x10, 0x00, 0x00, 0x00}, 0},
		{0x67, []byte{0x00, 0x3C, 0x00, 0x00, 0x00, 0x01, 0x54, 0x10, 0x32, 0x98}, 0},
		{0x74, []byte{0x10, 0x85, 0x80, 0x00, 0x00, 0x4E, 0x00}, 0},
		{0x98, []byte{0x3E, 0x07}, 0},
		{0x35, nil, 0},                      // Tearing effect on
		{0x21, nil, 0},                      // Display inversion on (needed by most GC9A01 panels)
		{0x11, nil, 120 * time.Millisecond}, // Sleep out
		{0x29, nil, 20 * time.Millisecond},  // Display on
	}

	for _, s := range seq {
		if err := d.writeCmd(s.cmd); err != nil {
			return err
		}
		if len(s.data) > 0 {
			if err := d.writeData(s.data...); err != nil {
				return err
			}
		}
		if s.wait > 0 {
			time.Sleep(s.wait)
		}
	}
	return nil
}

// setWindow sets the column/row address window before a pixel write.
func (d *GC9A01) setWindow(x0, y0, x1, y1 int) error {
	if err := d.writeCmd(0x2A); err != nil { // CASET
		return err
	}
	if err := d.writeData(byte(x0>>8), byte(x0), byte(x1>>8), byte(x1)); err != nil {
		return err
	}
	if err := d.writeCmd(0x2B); err != nil { // RASET
		return err
	}
	if err := d.writeData(byte(y0>>8), byte(y0), byte(y1>>8), byte(y1)); err != nil {
		return err
	}
	return d.writeCmd(0x2C) // RAMWR
}

// rgb565 packs an RGBA colour into a big-endian RGB565 word.
func rgb565(c color.RGBA) (byte, byte) {
	r := uint16(c.R) >> 3
	g := uint16(c.G) >> 2
	b := uint16(c.B) >> 3
	v := (r << 11) | (g << 5) | b
	return byte(v >> 8), byte(v)
}

// DrawFrame sends a full 240×240 RGBA image to the display as RGB565.
// SPI transfers are chunked to stay within the driver's buffer limits.
func (d *GC9A01) DrawFrame(img *image.RGBA) error {
	if err := d.setWindow(0, 0, W-1, H-1); err != nil {
		return err
	}
	if err := d.dc.Out(gpio.High); err != nil {
		return err
	}

	const chunkPx = 1024 // pixels per SPI transfer
	buf := make([]byte, chunkPx*2)
	total := W * H
	sent := 0

	for sent < total {
		n := total - sent
		if n > chunkPx {
			n = chunkPx
		}
		for i := 0; i < n; i++ {
			px := sent + i
			x := px % W
			y := px / W
			c := img.RGBAAt(x, y)
			buf[i*2], buf[i*2+1] = rgb565(c)
		}
		if err := d.conn.Tx(buf[:n*2], nil); err != nil {
			return err
		}
		sent += n
	}
	return nil
}

// ── Colour palette ────────────────────────────────────────────────────────────

var (
	colBackground = color.RGBA{10, 10, 30, 255}
	colSclera     = color.RGBA{240, 240, 230, 255}
	colIris       = color.RGBA{60, 130, 200, 255}
	colIrisDark   = color.RGBA{30, 80, 150, 255}
	colPupil      = color.RGBA{10, 10, 10, 255}
	colHighlight  = color.RGBA{255, 255, 255, 220}
	colLid        = color.RGBA{10, 10, 30, 255}
)

// ── Eye state ─────────────────────────────────────────────────────────────────

type EyeState struct {
	cx, cy    float64
	radius    float64
	blink     float64 // 0 = open, 1 = closed
	pupilX    float64 // −1..+1
	pupilY    float64
	irisFrac  float64
	pupilFrac float64
}

func newEye(cx, cy, radius float64) EyeState {
	return EyeState{cx: cx, cy: cy, radius: radius, irisFrac: 0.55, pupilFrac: 0.45}
}

// ── Rendering helpers ─────────────────────────────────────────────────────────

func blendPixel(img *image.RGBA, x, y int, c color.RGBA, a float64) {
	if x < 0 || y < 0 || x >= W || y >= H {
		return
	}
	base := img.RGBAAt(x, y)
	inv := 1 - a
	img.SetRGBA(x, y, color.RGBA{
		R: uint8(float64(c.R)*a + float64(base.R)*inv),
		G: uint8(float64(c.G)*a + float64(base.G)*inv),
		B: uint8(float64(c.B)*a + float64(base.B)*inv),
		A: 255,
	})
}

func filledCircle(img *image.RGBA, cx, cy, r float64, c color.RGBA) {
	x0 := int(math.Max(0, cx-r-1))
	y0 := int(math.Max(0, cy-r-1))
	x1 := int(math.Min(W-1, cx+r+1))
	y1 := int(math.Min(H-1, cy+r+1))
	r2 := r * r
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			dx := float64(x) - cx
			dy := float64(y) - cy
			d2 := dx*dx + dy*dy
			if d2 <= r2 {
				aa := math.Min(1.0, (r2-d2)/(2*r+1))
				blendPixel(img, x, y, c, aa)
			}
		}
	}
}

func drawLid(img *image.RGBA, e EyeState, bottom bool) {
	travel := e.radius * (0.15 + e.blink*0.85)
	r := e.radius
	for xi := int(e.cx - r - 1); xi <= int(e.cx+r+1); xi++ {
		if xi < 0 || xi >= W {
			continue
		}
		dx := float64(xi) - e.cx
		if math.Abs(dx) > r {
			continue
		}
		half := math.Sqrt(r*r - dx*dx)
		if bottom {
			yEdge := e.cy + half
			yLid := yEdge - travel
			for y := int(yLid); y <= int(yEdge)+1; y++ {
				blendPixel(img, xi, y, colLid, 1.0)
			}
		} else {
			yEdge := e.cy - half
			yLid := yEdge + travel
			for y := int(yEdge) - 1; y <= int(yLid); y++ {
				blendPixel(img, xi, y, colLid, 1.0)
			}
		}
	}
}

func drawEye(img *image.RGBA, e EyeState) {
	irisR := e.radius * e.irisFrac
	maxOff := e.radius * e.irisFrac * 0.35
	px := e.cx + e.pupilX*maxOff
	py := e.cy + e.pupilY*maxOff
	pupilR := irisR * e.pupilFrac

	filledCircle(img, e.cx, e.cy, e.radius, colSclera)
	filledCircle(img, px, py, irisR, colIrisDark)
	filledCircle(img, px-irisR*0.08, py-irisR*0.08, irisR*0.85, colIris)
	filledCircle(img, px, py, pupilR, colPupil)
	filledCircle(img, px-pupilR*0.35, py-pupilR*0.4, pupilR*0.28, colHighlight)
	drawLid(img, e, false)
	drawLid(img, e, true)
}

func renderFrame(left, right EyeState) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(img, img.Bounds(), &image.Uniform{colBackground}, image.Point{}, draw.Src)
	drawEye(img, left)
	drawEye(img, right)
	return img
}

// ── Animation ─────────────────────────────────────────────────────────────────

const dt = 1.0 / 30.0

type Animator struct {
	left, right            EyeState
	tPupilX, tPupilY       float64
	blinkPhase             float64
	blinking               bool
	nextBlink, nextSaccade time.Time
}

func newAnimator() *Animator {
	a := &Animator{
		left:  newEye(80, 120, 55),
		right: newEye(160, 120, 55),
	}
	a.nextBlink = time.Now().Add(scheduleDuration(2000, 4000))
	a.nextSaccade = time.Now().Add(scheduleDuration(800, 2000))
	return a
}

func scheduleDuration(baseMs, jitterMs int) time.Duration {
	return time.Duration(baseMs+rand.Intn(jitterMs)) * time.Millisecond
}

func (a *Animator) Step() {
	now := time.Now()

	// Saccade
	if now.After(a.nextSaccade) {
		a.tPupilX = (rand.Float64()*2 - 1) * 0.7
		a.tPupilY = (rand.Float64()*2 - 1) * 0.5
		a.nextSaccade = now.Add(scheduleDuration(800, 2000))
	}
	lerpF := 1 - math.Pow(0.05, dt*8)
	a.left.pupilX += (a.tPupilX - a.left.pupilX) * lerpF
	a.left.pupilY += (a.tPupilY - a.left.pupilY) * lerpF
	a.right.pupilX = a.left.pupilX
	a.right.pupilY = a.left.pupilY

	// Blink
	if !a.blinking && now.After(a.nextBlink) {
		a.blinking = true
		a.blinkPhase = 0
	}
	if a.blinking {
		a.blinkPhase += dt * 5.0
		if a.blinkPhase >= 1.0 {
			a.blinkPhase = 0
			a.blinking = false
			a.nextBlink = now.Add(scheduleDuration(2000, 4000))
		}
		blink := 1 - math.Abs(2*a.blinkPhase-1)
		a.left.blink = blink
		a.right.blink = blink
	} else {
		a.left.blink = 0
		a.right.blink = 0
	}
}
