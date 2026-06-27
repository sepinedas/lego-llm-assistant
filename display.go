// legoface drives two round GC9A01 240x240 LCDs over the same SPI bus and
// renders a bright cartoon face: an animated eye on one display and a
// mouth on the other. Three moods are supported: neutral, speaking, asleep.
//
// Wiring (Raspberry Pi defaults, both panels share everything except CS):
//
//	Panel signal | Pi pin            | Notes
//	-------------+-------------------+--------------------------------
//	VCC          | 3V3               |
//	GND          | GND               |
//	DIN / MOSI   | GPIO10  (SPI0)    | shared
//	CLK / SCLK   | GPIO11  (SPI0)    | shared
//	CS  (eye)    | GPIO8   (CE0)     | -> SPI0.0
//	CS  (mouth)  | GPIO7   (CE1)     | -> SPI0.1
//	DC  (eye)    | GPIO25            | per-panel (-dc-eye)
//	DC  (mouth)  | GPIO24            | per-panel (-dc-mouth)
//	RST          | GPIO27            | shared (-rst)
//	BL           | tie to 3V3        | no GPIO used
//
// Each panel now has its own DC line. RST is shared because the controllers
// only latch it while their own CS is asserted, and periph asserts CS per
// transaction. There is no backlight pin: tie BL high (or it's always-on).
//
// Build & run:
//
//	go mod init legoface
//	go get periph.io/x/conn/v3 periph.io/x/host/v3
//	go build -o legoface .
//	sudo ./legoface                 # auto-cycles the three moods
//	sudo ./legoface -state speaking  # lock one mood
//	sudo ./legoface -eye SPI0.1 -mouth SPI0.0   # swap displays
//
// If the image looks mirrored/upside down, change the MADCTL byte (0x36) in
// the init table below.
package main

import (
	"flag"
	"log"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

const (
	W = 240
	H = 240
)

// ---------------------------------------------------------------------------
// Colors (RGB565)
// ---------------------------------------------------------------------------

func rgb(r, g, b uint8) uint16 {
	return (uint16(r&0xF8) << 8) | (uint16(g&0xFC) << 3) | uint16(b>>3)
}

var (
	colBg      = rgb(16, 18, 22)    // off-screen / shutdown
	colHole    = rgb(34, 38, 40)    // (pin-hole interior, used by curvedBeam)
	colHoleRim = rgb(70, 76, 74)    // (pin-hole rim, used by curvedBeam)
	colIris    = rgb(70, 150, 235)  // cartoon iris (blue)
	colIris2   = rgb(38, 100, 195)  // iris rim (darker)
	colPupil   = rgb(8, 10, 12)     // pupil
	colCatch   = rgb(245, 248, 255) // catchlight
	colPink    = rgb(235, 120, 130) // rosy cheeks
	colPink2   = rgb(250, 178, 184) // cheek / tongue highlight
	colSkin    = rgb(250, 165, 55)  // cartoon "skin" filling each screen
	colSkinSh  = rgb(235, 155, 38)  // skin shadow / lip highlight
	colOutline = rgb(30, 26, 34)    // bold cartoon outline
	colWhite   = rgb(250, 250, 252) // eyeball / teeth
	colMouthIn = rgb(124, 36, 48)   // open-mouth interior
	colTongue  = rgb(236, 96, 110)  // tongue
	colZ       = rgb(120, 170, 235) // sleepy Z's
)

// ---------------------------------------------------------------------------
// Canvas: a uint16 RGB565 framebuffer with drawing primitives
// ---------------------------------------------------------------------------

type Canvas struct{ pix []uint16 }

func newCanvas() *Canvas { return &Canvas{pix: make([]uint16, W*H)} }

func (c *Canvas) clear(col uint16) {
	for i := range c.pix {
		c.pix[i] = col
	}
}

func (c *Canvas) fillRect(x, y, w, h int, col uint16) {
	for j := y; j < y+h; j++ {
		if j < 0 || j >= H {
			continue
		}
		base := j * W
		for i := x; i < x+w; i++ {
			if i < 0 || i >= W {
				continue
			}
			c.pix[base+i] = col
		}
	}
}

func (c *Canvas) fillCircle(cx, cy, r int, col uint16) {
	if r < 0 {
		return
	}
	r2 := r * r
	for y := -r; y <= r; y++ {
		py := cy + y
		if py < 0 || py >= H {
			continue
		}
		rem := r2 - y*y
		if rem < 0 {
			continue
		}
		xe := int(math.Sqrt(float64(rem)))
		x0, x1 := cx-xe, cx+xe
		if x0 < 0 {
			x0 = 0
		}
		if x1 >= W {
			x1 = W - 1
		}
		base := py * W
		for x := x0; x <= x1; x++ {
			c.pix[base+x] = col
		}
	}
}

// ring draws a filled annulus between rIn and rOut.
func (c *Canvas) ring(cx, cy, rOut, rIn int, col uint16) {
	ro2, ri2 := rOut*rOut, rIn*rIn
	for y := -rOut; y <= rOut; y++ {
		py := cy + y
		if py < 0 || py >= H {
			continue
		}
		yy := y * y
		base := py * W
		for x := -rOut; x <= rOut; x++ {
			d := x*x + yy
			if d <= ro2 && d >= ri2 {
				px := cx + x
				if px >= 0 && px < W {
					c.pix[base+px] = col
				}
			}
		}
	}
}

func (c *Canvas) fillRoundRect(x, y, w, h, r int, col uint16) {
	if 2*r > h {
		r = h / 2
	}
	if 2*r > w {
		r = w / 2
	}
	c.fillRect(x+r, y, w-2*r, h, col)
	c.fillRect(x, y+r, r, h-2*r, col)
	c.fillRect(x+w-r, y+r, r, h-2*r, col)
	c.fillCircle(x+r, y+r, r, col)
	c.fillCircle(x+w-r-1, y+r, r, col)
	c.fillCircle(x+r, y+h-r-1, r, col)
	c.fillCircle(x+w-r-1, y+h-r-1, r, col)
}

// ---------------------------------------------------------------------------
// Mood + rendering
// ---------------------------------------------------------------------------

type State int

const (
	Neutral State = iota
	Speaking
	Asleep
	Thinking
)

func drawEye(c *Canvas, st State, t float64) {
	c.clear(colSkin)
	cx, cy := W/2, H/2

	if st == Asleep {
		yoff := int(2 * math.Sin(t*1.6)) // gentle breathing
		// closed sleepy eye: a soft convex-up arc with two little lashes
		const er = 86
		bcy := cy + er + yoff // arc-circle centre below -> its top curves up
		c.curvedBeam(cx, bcy, er, 7, deg(236), deg(304), colOutline, false)
		for _, a := range []float64{deg(236), deg(304)} {
			ex := cx + int(float64(er)*math.Cos(a))
			ey := bcy + int(float64(er)*math.Sin(a))
			c.stroke(ex, ey, ex+int(15*math.Cos(a)), ey+int(15*math.Sin(a)), 4, colOutline)
		}
		// floating Z's drifting up and to the right
		base := math.Mod(t, 4.0) / 4.0
		for i := 0; i < 3; i++ {
			ph := math.Mod(base+float64(i)/3.0, 1.0)
			c.drawZ(150+int(ph*45), 96-int(ph*72), 8+i*3, 4, colZ)
		}
		return
	}

	blink := 0.0
	lookX, lookY := 0, 0
	brow := 0.25    // eyebrow raise
	browTilt := 0.0 // +ve = right end higher (furrowed inward)
	switch st {
	case Neutral:
		if ph := math.Mod(t, 3.5); ph < 0.18 {
			blink = math.Sin(ph / 0.18 * math.Pi)
		}
		lookX = int(7 * math.Sin(t*0.7))
		lookY = int(5 * math.Sin(t*0.9+1))
	case Speaking:
		if ph := math.Mod(t, 2.4); ph < 0.14 {
			blink = math.Sin(ph / 0.14 * math.Pi)
		}
		lookX = int(9 * math.Sin(t*1.5))
		lookY = int(3 * math.Sin(t*6))
		brow = 0.55 + 0.2*math.Sin(t*4) // lively raised brow while talking
	case Thinking:
		// slow blink every ~4 s
		if ph := math.Mod(t, 4.2); ph < 0.22 {
			blink = math.Sin(ph / 0.22 * math.Pi)
		}
		// gaze drifts up-left then occasionally flicks down-right as if scanning
		phase := math.Mod(t, 5.0)
		if phase < 3.5 {
			// locked up-left
			lookX = int(-28 + 3*math.Sin(t*0.4))
			lookY = int(-22 + 2*math.Sin(t*0.5))
		} else {
			// quick flick down-right then back
			f := (phase - 3.5) / 1.5
			lookX = int(-28 + 46*math.Sin(f*math.Pi))
			lookY = int(-22 + 30*math.Sin(f*math.Pi))
		}
		brow = 0.6                             // raised with concentration
		browTilt = 0.55 + 0.08*math.Sin(t*2.1) // furrowed, twitches slightly
	}

	const er = 86

	// eyebrow: tilted cartoon arc. browTilt > 0 furrows the inner (right) end down.
	browCy := cy - er - 12 - int(brow*16)
	tiltPx := int(browTilt * 14) // vertical offset of the inner end
	bx0 := cx - 52
	bx1 := cx + 52
	by0 := browCy + tiltPx // inner end (right side of brow)
	by1 := browCy - tiltPx // outer end (left side of brow)
	bmid := (by0 + by1) / 2
	// draw brow as three thick stroked segments for a smooth angled look
	c.stroke(bx0, by1, (bx0+bx1)/2, bmid-4, 7, colOutline)
	c.stroke((bx0+bx1)/2, bmid-4, bx1, by0, 7, colOutline)

	// eyeball: white with a bold black outline
	c.fillCircle(cx, cy, er+6, colOutline)
	c.fillCircle(cx, cy, er, colWhite)

	// iris, pupil and two glossy catchlights
	ix, iy := cx+lookX, cy+lookY+8
	ir := 48
	c.fillCircle(ix, iy, ir+4, colOutline)
	c.fillCircle(ix, iy, ir, colIris)
	c.ring(ix, iy, ir, ir-8, colIris2)
	c.fillCircle(ix, iy, 25, colPupil)
	c.fillCircle(ix-12, iy-14, 11, colCatch)
	c.fillCircle(ix+11, iy+9, 5, colCatch)

	// blink: skin-colored lids sweep in from top and bottom with a lash line
	if blink > 0.01 {
		drawLid(c, cx, cy, er, true, blink)
		drawLid(c, cx, cy, er, false, blink)
	}
}

// drawLid sweeps a smooth skin eyelid over the eye. p: 0 = open, 1 = shut.
func drawLid(c *Canvas, cx, cy, er int, top bool, p float64) {
	const LR = 280 // big radius -> the lid edge is a gentle curve
	if top {
		edge := lerpi(cy-er-14, cy+3, p)
		lcy := edge - LR
		c.fillCircle(cx, lcy, LR, colSkin)
		c.curvedBeam(cx, lcy, LR, 4, deg(90-7), deg(90+7), colOutline, false)
	} else {
		edge := lerpi(cy+er+14, cy-3, p)
		lcy := edge + LR
		c.fillCircle(cx, lcy, LR, colSkin)
		c.curvedBeam(cx, lcy, LR, 3, deg(270-7), deg(270+7), colOutline, false)
	}
}

func deg(d float64) float64 { return d * math.Pi / 180 }

func lerpi(a, b int, t float64) int { return a + int(float64(b-a)*t) }

// curvedBeam stamps a thick rounded stroke along a circular arc (center
// cx,cy, radius r, sweeping a0->a1 radians), optionally with Technic pin
// holes spaced along it.
func (c *Canvas) curvedBeam(cx, cy, r, half int, a0, a1 float64, col uint16, holes bool) {
	arcLen := math.Abs(a1-a0) * float64(r)
	step := half / 2
	if step < 1 {
		step = 1
	}
	steps := int(arcLen)/step + 1
	for i := 0; i <= steps; i++ {
		a := a0 + (a1-a0)*float64(i)/float64(steps)
		x := cx + int(math.Round(float64(r)*math.Cos(a)))
		y := cy + int(math.Round(float64(r)*math.Sin(a)))
		c.fillCircle(x, y, half, col)
	}
	if holes && half >= 6 {
		hr := half * 42 / 100
		nh := int(arcLen / (float64(half) * 2.4))
		if nh < 1 {
			nh = 1
		}
		for k := 0; k <= nh; k++ {
			f := float64(k) / float64(nh)
			if f < 0.08 || f > 0.92 { // keep holes off the very ends
				continue
			}
			a := a0 + (a1-a0)*f
			x := cx + int(float64(r)*math.Cos(a))
			y := cy + int(float64(r)*math.Sin(a))
			c.fillCircle(x, y, hr+1, colHoleRim)
			c.fillCircle(x, y, hr-1, colHole)
		}
	}
}

// drawCheeks adds two rosy cartoon cheeks flanking the mouth.
func (c *Canvas) drawCheeks(cx, cy int) {
	for _, sx := range []int{-1, 1} {
		x := cx + sx*90
		c.fillCircle(x, cy, 18, colPink)
		c.fillCircle(x-sx*6, cy-5, 6, colPink2)
	}
}

// stroke stamps a thick rounded line from (x0,y0) to (x1,y1).
func (c *Canvas) stroke(x0, y0, x1, y1, th int, col uint16) {
	dx, dy := x1-x0, y1-y0
	n := int(math.Hypot(float64(dx), float64(dy)))/2 + 1
	for i := 0; i <= n; i++ {
		f := float64(i) / float64(n)
		c.fillCircle(x0+int(float64(dx)*f), y0+int(float64(dy)*f), th, col)
	}
}

// drawZ draws a slanted cartoon "Z" centered at (cx,cy) with half-size s.
func (c *Canvas) drawZ(cx, cy, s, th int, col uint16) {
	x0, x1 := cx-s, cx+s
	yT, yB := cy-s, cy+s
	c.stroke(x0, yT, x1, yT, th, col)
	c.stroke(x1, yT, x0, yB, th, col)
	c.stroke(x0, yB, x1, yB, th, col)
}

// drawSmile renders a bold cartoon smile. open: 0 = closed grin, 1 = wide
// open. yoff nudges the whole mouth (used for sleepy breathing).
func drawSmile(c *Canvas, open float64, yoff int) {
	if open < 0 {
		open = 0
	}
	if open > 1 {
		open = 1
	}
	const R = 100
	cx := W / 2
	cy := H/2 - 64 + yoff       // arc centre above -> the smile curves up at ends
	a0, a1 := deg(30), deg(150) // wide grin
	lip := 9

	c.drawCheeks(cx, H/2+20+yoff)

	if open <= 0.06 {
		// closed cartoon smile: one bold stroke with a soft highlight
		c.curvedBeam(cx, cy, R, lip, a0, a1, colOutline, false)
		c.curvedBeam(cx, cy, R-lip+2, 2, deg(44), deg(136), colSkinSh, false)
		return
	}

	gap := 8 + int(open*30)

	// bold black outline shell, then the dark interior inset within it
	c.curvedBeam(cx, cy, R, gap+lip, a0, a1, colOutline, false)
	c.curvedBeam(cx, cy, R, gap, deg(34), deg(146), colMouthIn, false)

	// smooth white upper teeth; lower teeth appear on a big grin
	c.curvedBeam(cx, cy, R-gap+6, 7, deg(40), deg(140), colWhite, false)
	if open > 0.55 {
		c.curvedBeam(cx, cy, R+gap-6, 5, deg(48), deg(132), colWhite, false)
	}
	// tongue with a little sheen
	if open > 0.4 {
		c.curvedBeam(cx, cy, R+gap-9, gap*55/100, deg(62), deg(118), colTongue, false)
		c.curvedBeam(cx, cy, R+gap-9, gap*20/100, deg(80), deg(100), colPink2, false)
	}
}

// drawPursedMouth renders a small side-shifted squiggly mouth for thinking.
// It slowly drifts left/right and has a subtle squiggle that wriggles over time.
func drawPursedMouth(c *Canvas, t float64) {
	cx := W/2 + int(18*math.Sin(t*0.8)) // drifts left and right
	cy := H/2 + 44

	// outline shell
	c.fillCircle(cx, cy, 26, colOutline)
	// skin fill — a slightly squashed oval
	for dy := -14; dy <= 14; dy++ {
		xw := int(math.Sqrt(math.Max(0, float64(18*18-dy*dy*18*18/14/14))))
		for dx := -xw; dx <= xw; dx++ {
			px, py := cx+dx, cy+dy
			if px >= 0 && px < W && py >= 0 && py < H {
				c.pix[py*W+px] = colSkin
			}
		}
	}

	// three-point squiggle line across the mouth
	pts := [4][2]int{
		{cx - 18, cy + int(5*math.Sin(t*3.1+0))},
		{cx - 6, cy + int(6*math.Sin(t*3.1+1.2))},
		{cx + 6, cy + int(6*math.Sin(t*3.1+2.4))},
		{cx + 18, cy + int(5*math.Sin(t*3.1+3.6))},
	}
	for i := 0; i < 3; i++ {
		c.stroke(pts[i][0], pts[i][1], pts[i+1][0], pts[i+1][1], 4, colOutline)
	}

	// small highlight dot at upper-right to give it a cartoon lip volume
	c.fillCircle(cx+8, cy-6, 4, colSkinSh)
}

func drawMouth(c *Canvas, st State, t float64) {
	c.clear(colSkin)

	switch st {
	case Neutral:
		drawSmile(c, 0.26, 0) // friendly resting grin showing teeth
	case Speaking:
		open := 0.5 + 0.5*math.Sin(t*9)
		open *= 0.35 + 0.65*math.Abs(math.Sin(t*3.3)) // syllable-ish envelope
		if open < 0.18 {
			open = 0.18 // keep a smile even between syllables
		}
		drawSmile(c, open, 0)
	case Asleep:
		yoff := int(2 * math.Sin(t*1.6)) // gentle breathing
		drawSmile(c, 0.0, yoff)          // peaceful closed smile
	case Thinking:
		drawPursedMouth(c, t)
	}
}

// ---------------------------------------------------------------------------
// GC9A01 driver over periph SPI
// ---------------------------------------------------------------------------

type initStep struct {
	cmd   byte
	data  []byte
	delay time.Duration
}

// Canonical GC9A01A power-on sequence. 0x36 = MADCTL (orientation/BGR),
// 0x3A = COLMOD (0x05 -> 16bpp RGB565).
var initSeq = []initStep{
	{cmd: 0xEF},
	{cmd: 0xEB, data: []byte{0x14}},
	{cmd: 0xFE},
	{cmd: 0xEF},
	{cmd: 0xEB, data: []byte{0x14}},
	{cmd: 0x84, data: []byte{0x40}},
	{cmd: 0x85, data: []byte{0xFF}},
	{cmd: 0x86, data: []byte{0xFF}},
	{cmd: 0x87, data: []byte{0xFF}},
	{cmd: 0x88, data: []byte{0x0A}},
	{cmd: 0x89, data: []byte{0x21}},
	{cmd: 0x8A, data: []byte{0x00}},
	{cmd: 0x8B, data: []byte{0x80}},
	{cmd: 0x8C, data: []byte{0x01}},
	{cmd: 0x8D, data: []byte{0x01}},
	{cmd: 0x8E, data: []byte{0xFF}},
	{cmd: 0x8F, data: []byte{0xFF}},
	{cmd: 0xB6, data: []byte{0x00, 0x20}},
	{cmd: 0x36, data: []byte{0x48}}, // MADCTL: MX | BGR
	{cmd: 0x3A, data: []byte{0x05}}, // COLMOD: 16bpp
	{cmd: 0x90, data: []byte{0x08, 0x08, 0x08, 0x08}},
	{cmd: 0xBD, data: []byte{0x06}},
	{cmd: 0xBC, data: []byte{0x00}},
	{cmd: 0xFF, data: []byte{0x60, 0x01, 0x04}},
	{cmd: 0xC3, data: []byte{0x13}},
	{cmd: 0xC4, data: []byte{0x13}},
	{cmd: 0xC9, data: []byte{0x22}},
	{cmd: 0xBE, data: []byte{0x11}},
	{cmd: 0xE1, data: []byte{0x10, 0x0E}},
	{cmd: 0xDF, data: []byte{0x21, 0x0C, 0x02}},
	{cmd: 0xF0, data: []byte{0x45, 0x09, 0x08, 0x08, 0x26, 0x2A}},
	{cmd: 0xF1, data: []byte{0x43, 0x70, 0x72, 0x36, 0x37, 0x6F}},
	{cmd: 0xF2, data: []byte{0x45, 0x09, 0x08, 0x08, 0x26, 0x2A}},
	{cmd: 0xF3, data: []byte{0x43, 0x70, 0x72, 0x36, 0x37, 0x6F}},
	{cmd: 0xED, data: []byte{0x1B, 0x0B}},
	{cmd: 0xAE, data: []byte{0x77}},
	{cmd: 0xCD, data: []byte{0x63}},
	{cmd: 0x70, data: []byte{0x07, 0x07, 0x04, 0x0E, 0x0F, 0x09, 0x07, 0x08, 0x03}},
	{cmd: 0xE8, data: []byte{0x34}},
	{cmd: 0x62, data: []byte{0x18, 0x0D, 0x71, 0xED, 0x70, 0x70, 0x18, 0x0F, 0x71, 0xEF, 0x70, 0x70}},
	{cmd: 0x63, data: []byte{0x18, 0x11, 0x71, 0xF1, 0x70, 0x70, 0x18, 0x13, 0x71, 0xF3, 0x70, 0x70}},
	{cmd: 0x64, data: []byte{0x28, 0x29, 0xF1, 0x01, 0xF1, 0x00, 0x07}},
	{cmd: 0x66, data: []byte{0x3C, 0x00, 0xCD, 0x67, 0x45, 0x45, 0x10, 0x00, 0x00, 0x00}},
	{cmd: 0x67, data: []byte{0x00, 0x3C, 0x00, 0x00, 0x00, 0x01, 0x54, 0x10, 0x32, 0x98}},
	{cmd: 0x74, data: []byte{0x10, 0x85, 0x80, 0x00, 0x00, 0x4E, 0x00}},
	{cmd: 0x98, data: []byte{0x3E, 0x07}},
	{cmd: 0x35},
	{cmd: 0x21},
	{cmd: 0x11, delay: 120 * time.Millisecond},
	{cmd: 0x29, delay: 20 * time.Millisecond},
}

type Display struct {
	name   string
	port   spi.PortCloser
	c      spi.Conn
	dc     gpio.PinOut // per-panel
	canvas *Canvas
	buf    []byte
}

func openDisplay(name string, mhz int, dc gpio.PinOut) *Display {
	p, err := spireg.Open(name)
	if err != nil {
		log.Fatalf("open %s: %v", name, err)
	}
	conn, err := p.Connect(physic.Frequency(mhz)*physic.MegaHertz, spi.Mode0, 8)
	if err != nil {
		log.Fatalf("connect %s: %v", name, err)
	}
	return &Display{
		name:   name,
		port:   p,
		c:      conn,
		dc:     dc,
		canvas: newCanvas(),
		buf:    make([]byte, W*H*2),
	}
}

func (d *Display) cmd(b byte) {
	_ = d.dc.Out(gpio.Low)
	if err := d.c.Tx([]byte{b}, nil); err != nil {
		log.Printf("%s cmd %#x: %v", d.name, b, err)
	}
}

func (d *Display) data(b ...byte) {
	if len(b) == 0 {
		return
	}
	_ = d.dc.Out(gpio.High)
	if err := d.c.Tx(b, nil); err != nil {
		log.Printf("%s data: %v", d.name, err)
	}
}

func (d *Display) sendInit() {
	for _, s := range initSeq {
		d.cmd(s.cmd)
		d.data(s.data...)
		if s.delay > 0 {
			time.Sleep(s.delay)
		}
	}
}

func (d *Display) setWindow(x0, y0, x1, y1 int) {
	d.cmd(0x2A)
	d.data(byte(x0>>8), byte(x0), byte(x1>>8), byte(x1))
	d.cmd(0x2B)
	d.data(byte(y0>>8), byte(y0), byte(y1>>8), byte(y1))
}

func (d *Display) flush() {
	d.setWindow(0, 0, W-1, H-1)
	d.cmd(0x2C) // RAMWR
	for i, v := range d.canvas.pix {
		d.buf[2*i] = byte(v >> 8)
		d.buf[2*i+1] = byte(v)
	}
	_ = d.dc.Out(gpio.High)
	const chunk = 4096 // stay within the default spidev bufsiz
	for off := 0; off < len(d.buf); off += chunk {
		end := off + chunk
		if end > len(d.buf) {
			end = len(d.buf)
		}
		if err := d.c.Tx(d.buf[off:end], nil); err != nil {
			log.Printf("%s flush: %v", d.name, err)
			return
		}
	}
}

func (d *Display) close() { _ = d.port.Close() }

// ---------------------------------------------------------------------------
// GPIO helpers and main loop
// ---------------------------------------------------------------------------

func mustPinOut(name string) gpio.PinOut {
	p := gpioreg.ByName(name)
	if p == nil {
		log.Fatalf("gpio %q not found", name)
	}
	if err := p.Out(gpio.High); err != nil {
		log.Fatalf("gpio %q -> out: %v", name, err)
	}
	return p
}

func hwReset(rst gpio.PinOut) {
	_ = rst.Out(gpio.High)
	time.Sleep(10 * time.Millisecond)
	_ = rst.Out(gpio.Low)
	time.Sleep(20 * time.Millisecond)
	_ = rst.Out(gpio.High)
	time.Sleep(120 * time.Millisecond)
}

func currentState(lock string, t float64) State {
	switch lock {
	case "neutral":
		return Neutral
	case "speaking":
		return Speaking
	case "asleep":
		return Asleep
	case "thinking":
		return Thinking
	}
	switch int(t/5) % 4 { // auto-cycle, 5s per mood
	case 0:
		return Neutral
	case 1:
		return Speaking
	case 2:
		return Asleep
	default:
		return Thinking
	}
}

func initDisplay(lock *string) {
	eyePort := flag.String("eye", "SPI0.0", "SPI port for the eye display")
	mouthPort := flag.String("mouth", "SPI0.1", "SPI port for the mouth display")
	dcEyeName := flag.String("dc-eye", "GPIO22", "Data/Command GPIO for the eye display")
	dcMouthName := flag.String("dc-mouth", "GPIO25", "Data/Command GPIO for the mouth display")
	rstName := flag.String("rst", "GPIO27", "shared Reset GPIO")
	hz := flag.Int("hz", 40, "SPI clock in MHz")
	fps := flag.Int("fps", 30, "target frames per second")
	flag.Parse()

	if _, err := host.Init(); err != nil {
		log.Fatalf("periph init: %v", err)
	}

	dcEye := mustPinOut(*dcEyeName)
	dcMouth := mustPinOut(*dcMouthName)
	rst := mustPinOut(*rstName)

	eye := openDisplay(*eyePort, *hz, dcEye)
	mouth := openDisplay(*mouthPort, *hz, dcMouth)
	defer eye.close()
	defer mouth.close()
	log.Printf("eye=%s mouth=%s @ %dMHz", *eyePort, *mouthPort, *hz)

	hwReset(rst) // shared reset, then init each panel
	eye.sendInit()
	mouth.sendInit()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	start := time.Now()
	frame := time.Second / time.Duration(*fps)
	for {
		select {
		case <-sig:
			eye.canvas.clear(colBg)
			eye.flush()
			mouth.canvas.clear(colBg)
			mouth.flush()
			log.Println("bye")
			return
		default:
		}

		now := time.Now()
		t := now.Sub(start).Seconds()
		st := currentState(*lock, t)
		drawEye(eye.canvas, st, t)
		drawMouth(mouth.canvas, st, t)
		eye.flush()
		mouth.flush()

		if d := frame - time.Since(now); d > 0 {
			time.Sleep(d)
		}
	}
}
