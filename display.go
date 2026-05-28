// legoeyes — animated LEGO-brick eyes on two GC9A01 (240×240) circular LCDs.
//
// Wiring (Raspberry Pi header):
//
//   Signal     │ RPi pin │ Both displays unless noted
//   ───────────┼─────────┼──────────────────────────
//   SPI MOSI   │ GPIO10  │ SDA  (shared)
//   SPI SCLK   │ GPIO11  │ SCL  (shared)
//   CS left    │ GPIO8   │ CS   (left eye  – SPI0.0)
//   CS right   │ GPIO7   │ CS   (right eye – SPI0.1)
//   DC left    │ GPIO24  │ D/C  (left eye)
//   DC right   │ GPIO25  │ D/C  (right eye)
//   RST        │ GPIO23  │ RST  (shared, tie together)
//   3.3 V      │ pin 1   │ VCC
//   GND        │ pin 6   │ GND
//
// Enable SPI on the Pi:  sudo raspi-config → Interface Options → SPI → Yes
// Build:  go build -o legoeyes .
// Run:    sudo ./legoeyes
//
// The animation cycles automatically (adjustable via stateCycle in main):
//   Neutral (5 s) → Speaking (5 s) → Asleep (6 s) → repeat
// Ctrl-C exits cleanly.

package main

import (
	"log"
	"math"
	"math/rand"
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

// ╔══════════════════════════════════════════════════════════════════════════╗
// ║  Display constants                                                        ║
// ╚══════════════════════════════════════════════════════════════════════════╝

const (
	screenW   = 240                   // GC9A01 width  (pixels)
	screenH   = 240                   // GC9A01 height (pixels)
	brickPx   = 10                    // pixels per LEGO brick → 24 × 24 grid
	gridN     = screenW / brickPx     // = 24
	frameSize = screenW * screenH * 2 // RGB565 bytes per full frame

	targetFPS = 30
	frameDT   = 1.0 / float64(targetFPS)

	// Eye geometry (brick-unit values; gridN=24, scaled ×2 vs 12-grid)
	outerR   = 11.0 // edge of rendered circle (corners → background)
	rimR     = 9.0  // outer sclera edge (narrow dark ring inside outerR)
	irisRst  = 4.6  // iris radius at rest
	pupilRst = 2.6  // pupil radius at rest

	// Mouth geometry (brick units, ×2 scale vs 12-grid)
	mouthOvalW = 10.4 // oval half-width
	mouthOvalH = 8.0  // oval half-height
	mouthLipT  = 3.5  // lip thickness — thin
)

// ╔══════════════════════════════════════════════════════════════════════════╗
// ║  Colour helpers                                                           ║
// ╚══════════════════════════════════════════════════════════════════════════╝

type col struct{ r, g, b uint8 }

func sat8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func (c col) dim(n int) col { return col{sat8(int(c.r) - n), sat8(int(c.g) - n), sat8(int(c.b) - n)} }

func (c col) glow(n int) col { return col{sat8(int(c.r) + n), sat8(int(c.g) + n), sat8(int(c.b) + n)} }

func mix(a, b col, t float64) col {
	blend := func(x, y uint8) uint8 {
		return sat8(int(float64(x)*(1-t) + float64(y)*t))
	}
	return col{blend(a.r, b.r), blend(a.g, b.g), blend(a.b, b.b)}
}

// rgb565 encodes to big-endian 16-bit RGB565 (as used by GC9A01).
func (c col) rgb565() (byte, byte) {
	v := uint16(c.r>>3)<<11 | uint16(c.g>>2)<<5 | uint16(c.b>>3)
	return byte(v >> 8), byte(v)
}

// ── LEGO palette ──────────────────────────────────────────────────────────
var (
	colBg      = col{18, 18, 30}    // eye socket / display corners (dark navy)
	colLid     = col{52, 46, 82}    // eyelid surface – distinct purple-navy
	colLidEdge = col{75, 68, 112}   // lash-line highlight at the lid boundary
	colSclera  = col{248, 248, 252} // white of eye
	colIris    = col{25, 102, 212}  // resting LEGO blue iris
	colIrisFx  = col{55, 160, 255}  // speaking iris glow (brighter blue)
	colPupil   = col{8, 8, 14}      // near-black pupil
	colHilite  = col{255, 255, 255} // corneal highlight

	// Mouth palette
	colLip          = col{200, 78, 88}   // LEGO red-pink lip
	colTeeth        = col{245, 245, 250} // white teeth
	colMouth        = col{20, 7, 9}      // dark inner mouth cavity
	colTongue       = col{218, 112, 118} // pink tongue
	colTongueGroove = col{185, 88, 93}   // darker median sulcus groove
)

// ╔══════════════════════════════════════════════════════════════════════════╗
// ║  GC9A01 driver                                                            ║
// ╚══════════════════════════════════════════════════════════════════════════╝

// Display wraps a single GC9A01 over periph.io SPI.
type Display struct {
	conn spi.Conn
	dc   gpio.PinIO
}

func newDisplay(conn spi.Conn, dc gpio.PinIO) *Display { return &Display{conn: conn, dc: dc} }

func (d *Display) cmd(c byte) {
	_ = d.dc.Out(gpio.Low)
	_ = d.conn.Tx([]byte{c}, nil)
}

func (d *Display) data(v ...byte) {
	_ = d.dc.Out(gpio.High)
	_ = d.conn.Tx(v, nil)
}

func (d *Display) reg(cmd byte, args ...byte) {
	d.cmd(cmd)
	if len(args) > 0 {
		d.data(args...)
	}
}

// Init sends the full GC9A01 power-on initialisation sequence.
// A hardware reset must be performed before calling Init.
func (d *Display) Init() {
	d.cmd(0xEF)
	d.reg(0xEB, 0x14)
	d.cmd(0xFE)
	d.cmd(0xEF)
	d.reg(0xEB, 0x14)
	d.reg(0x84, 0x40)
	d.reg(0x85, 0xFF)
	d.reg(0x86, 0xFF)
	d.reg(0x87, 0xFF)
	d.reg(0x88, 0x0A)
	d.reg(0x89, 0x21)
	d.reg(0x8A, 0x00)
	d.reg(0x8B, 0x80)
	d.reg(0x8C, 0x01)
	d.reg(0x8D, 0x01)
	d.reg(0x8E, 0xFF)
	d.reg(0x8F, 0xFF)
	d.reg(0xB6, 0x00, 0x20) // Display Function Control
	d.reg(0x36, 0x08)       // MADCTL – normal scan direction, RGB order
	d.reg(0x3A, 0x05)       // Interface Pixel Format: 16-bit RGB565
	d.reg(0x90, 0x08, 0x08, 0x08, 0x08)
	d.reg(0xBD, 0x06)
	d.reg(0xBC, 0x00)
	d.reg(0xFF, 0x60, 0x01, 0x04)
	d.reg(0xC3, 0x13) // Power Control 2
	d.reg(0xC4, 0x13) // Power Control 3
	d.reg(0xC9, 0x22)
	d.reg(0xBE, 0x11)
	d.reg(0xE1, 0x10, 0x0E)
	d.reg(0xDF, 0x21, 0x0C, 0x02)
	d.reg(0xF0, 0x45, 0x09, 0x08, 0x08, 0x26, 0x2A) // Gamma (+)
	d.reg(0xF1, 0x43, 0x70, 0x72, 0x36, 0x37, 0x6F)
	d.reg(0xF2, 0x45, 0x09, 0x08, 0x08, 0x26, 0x2A) // Gamma (−)
	d.reg(0xF3, 0x43, 0x70, 0x72, 0x36, 0x37, 0x6F)
	d.reg(0xED, 0x1B, 0x0B)
	d.reg(0xAE, 0x77)
	d.reg(0xCD, 0x63)
	d.reg(0x70, 0x07, 0x07, 0x04, 0x0E, 0x0F, 0x09, 0x07, 0x08, 0x03)
	d.reg(0xE8, 0x34)
	d.reg(0x62, 0x18, 0x0D, 0x71, 0xED, 0x70, 0x70, 0x18, 0x0F, 0x71, 0xEF, 0x70, 0x70)
	d.reg(0x63, 0x18, 0x11, 0x71, 0xF1, 0x70, 0x70, 0x18, 0x13, 0x71, 0xF3, 0x70, 0x70)
	d.reg(0x64, 0x28, 0x29, 0xF1, 0x01, 0xF1, 0x00, 0x07)
	d.reg(0x66, 0x3C, 0x00, 0xCD, 0x67, 0x45, 0x45, 0x10, 0x00, 0x00, 0x00)
	d.reg(0x67, 0x00, 0x3C, 0x00, 0x00, 0x00, 0x01, 0x54, 0x10, 0x32, 0x98)
	d.reg(0x74, 0x10, 0x85, 0x80, 0x00, 0x00, 0x4E, 0x00)
	d.reg(0x98, 0x3E, 0x07)
	d.cmd(0x35) // Tearing Effect Line ON
	d.cmd(0x21) // Display Inversion ON  (required by most GC9A01 modules)
	d.cmd(0x11) // Sleep Out
	time.Sleep(120 * time.Millisecond)
	d.cmd(0x29) // Display ON
	time.Sleep(20 * time.Millisecond)
}

// Flush sends a full RGB565 frame buffer to the display in 4 KiB chunks
// (Linux SPI buffers are typically limited to 4096 bytes per transfer).
func (d *Display) Flush(buf []byte) {
	d.cmd(0x2A) // Column Address Set: 0 … 239
	d.data(0x00, 0x00, 0x00, 0xEF)
	d.cmd(0x2B) // Row Address Set: 0 … 239
	d.data(0x00, 0x00, 0x00, 0xEF)
	d.cmd(0x2C) // Memory Write – enter pixel-data mode
	_ = d.dc.Out(gpio.High)
	const chunkSz = 4096
	for off := 0; off < len(buf); off += chunkSz {
		end := off + chunkSz
		if end > len(buf) {
			end = len(buf)
		}
		_ = d.conn.Tx(buf[off:end], nil)
	}
}

// ╔══════════════════════════════════════════════════════════════════════════╗
// ║  LEGO-brick eye renderer                                                  ║
// ╚══════════════════════════════════════════════════════════════════════════╝

// EyeParams holds all animatable eye state for one eye.
type EyeParams struct {
	EyelidT    float64 // 0 = fully open → 1 = top lid fully closed
	BottomLidT float64 // 0 = fully open → 1 = bottom lid fully closed
	PupilDX    float64 // gaze offset in brick-units (right = positive)
	PupilDY    float64 // gaze offset in brick-units (down  = positive)
	IrisR      float64 // iris radius  (brick-units)
	PupilR     float64 // pupil radius (brick-units)
	IrisBright float64 // 0 = resting blue → 1 = speaking glow
}

func restingParams() EyeParams {
	return EyeParams{IrisR: irisRst, PupilR: pupilRst}
}

// Eye centre in float brick-space.
const (
	eyeCX = float64(gridN) / 2.0 // = 6.0
	eyeCY = float64(gridN) / 2.0
)

// brickBase returns the LEGO palette colour for brick (bx, by).
// It evaluates the eye anatomy layers from outside inward.
func brickBase(bx, by int, p EyeParams) col {
	bfx := float64(bx) + 0.5 // brick centre, floating
	bfy := float64(by) + 0.5

	// ── 1. Circular boundary of the round display ───────────────────────
	dx := bfx - eyeCX
	dy := bfy - eyeCY
	eyeDist := math.Sqrt(dx*dx + dy*dy)
	if eyeDist > outerR {
		return colBg
	}

	// ── 2. Dual eyelids ──────────────────────────────────────────────────
	//   topLidThresh: threshold below which the top lid covers (from top down).
	//   botLidThresh: threshold above which the bottom lid covers (from bottom up).
	//   At eyelidT=0.54, bottomLidT=0.46 the two thresholds meet (~eyeCY+0.04)
	//   so the eye is fully and cleanly closed.
	topLidThresh := eyeCY - outerR + p.EyelidT*(2*outerR)
	botLidThresh := eyeCY + outerR - p.BottomLidT*(2*outerR)

	if bfy < topLidThresh {
		// Lash-line: the final brick row just before the boundary gets a bright edge.
		if bfy >= topLidThresh-1.0 {
			return colLidEdge
		}
		return colLid
	}
	if bfy > botLidThresh {
		if bfy <= botLidThresh+1.0 {
			return colLidEdge
		}
		return colLid
	}

	// ── 3. Narrow dark ring (eye socket rim between outerR and rimR) ─────
	if eyeDist > rimR {
		return colBg
	}

	// ── 4. Pupil & iris (pupil centre can shift for gaze direction) ──────
	pcx := eyeCX + p.PupilDX
	pcy := eyeCY + p.PupilDY
	pdx := bfx - pcx
	pdy := bfy - pcy
	pd := math.Sqrt(pdx*pdx + pdy*pdy)

	if pd < p.PupilR {
		// Corneal highlight: one ~brick-sized spot, upper-left of pupil
		hdx := bfx - (pcx - 1.3)
		hdy := bfy - (pcy - 1.3)
		if math.Sqrt(hdx*hdx+hdy*hdy) < 1.1 {
			return colHilite
		}
		return colPupil
	}

	if pd < p.IrisR {
		// Lerp from resting blue to speaking glow
		return mix(colIris, colIrisFx, p.IrisBright)
	}

	return colSclera
}

// brickPixel applies a LEGO brick surface texture:
//   - 1-pixel bevel (bright top-left, dark bottom-right)
//   - circular stud in the centre of each brick
func brickPixel(base col, lx, ly int) col {
	// ── Bevel ─────────────────────────────────────────────────────────────
	if lx == brickPx-1 || ly == brickPx-1 {
		return base.dim(32) // shadow: right & bottom edges
	}
	if lx == 0 || ly == 0 {
		return base.glow(40) // highlight: top & left edges
	}

	// ── Circular stud centred in each brick ───────────────────────────────
	//   Proportioned for 5 px bricks: outer ring r≈1.55, inner top r≈1.1.
	scx := float64(brickPx)/2.0 - 0.5
	scy := float64(brickPx)/2.0 - 0.5
	sdx := float64(lx) - scx
	sdy := float64(ly) - scy
	sd := math.Sqrt(sdx*sdx + sdy*sdy)
	if sd < 2.9 {
		if sd < 1.9 {
			return base.dim(11) // stud top-face (subtle inset)
		}
		return base.glow(42) // stud side-wall (lit by the LEGO sun)
	}

	return base
}

// RenderEye writes a full 240×240 RGB565 frame into buf.
// buf must be at least frameSize bytes.
func RenderEye(p EyeParams, buf []byte) {
	for y := 0; y < screenH; y++ {
		for x := 0; x < screenW; x++ {
			base := brickBase(x/brickPx, y/brickPx, p)
			c := brickPixel(base, x%brickPx, y%brickPx)
			hi, lo := c.rgb565()
			i := (y*screenW + x) * 2
			buf[i] = hi
			buf[i+1] = lo
		}
	}
}

// ╔══════════════════════════════════════════════════════════════════════════╗
// ║  LEGO-brick mouth renderer                                                ║
// ╚══════════════════════════════════════════════════════════════════════════╝

// MouthParams holds all animatable mouth state.
type MouthParams struct {
	OpenT  float64 // 0 = closed,  1 = wide open (lower jaw drops)
	SmileT float64 // -1 = frown,  0 = neutral,  +1 = full smile
}

func restingMouthParams() MouthParams {
	return MouthParams{SmileT: 0.5}
}

// mouthBrickBase returns the palette colour for mouth brick (bx, by).
func mouthBrickBase(bx, by int, mp MouthParams) col {
	bfx := float64(bx) + 0.5
	bfy := float64(by) + 0.5
	dx := bfx - eyeCX
	dy := bfy - eyeCY

	// ── Oval outer boundary ──────────────────────────────────────────────
	odx := dx / mouthOvalW
	ody := dy / mouthOvalH
	if odx*odx+ody*ody > 1.0 {
		return colBg
	}

	// ── Smile parabola: positive SmileT lifts corners upward ─────────────
	xNorm := dx / mouthOvalW                 // -1 … +1 across the oval
	smile := mp.SmileT * 2.8 * xNorm * xNorm // coeff scaled ×2 vs 12-grid

	upperTop := eyeCY - 3.2 - smile     // top of upper lip
	upperBot := upperTop + mouthLipT    // bottom of upper lip / top of gap
	lowerTop := upperBot + mp.OpenT*4.8 // top of lower lip (jaw drops by OpenT)
	lowerBot := lowerTop + mouthLipT    // bottom of lower lip

	switch {
	case bfy < upperTop:
		return colBg // above mouth
	case bfy < upperBot:
		return colLip // upper lip
	case bfy < lowerTop:
		// Inside the mouth gap
		if mp.OpenT < 0.04 {
			return colLip // barely open — lips appear closed
		}
		relY := (bfy - upperBot) / (lowerTop - upperBot) // 0=top, 1=bottom of gap

		// Upper teeth (top 35% of gap)
		if relY < 0.35 {
			return colTeeth
		}

		// Tongue: visible as soon as the mouth is open
		if relY > 0.50 && mp.OpenT > 0.10 {
			// Tongue narrows the closer to the tip; widens toward the base.
			tongueEdge := mouthOvalW * 0.72 * (0.75 + (relY-0.50)/0.50*0.25)
			if math.Abs(dx) > tongueEdge {
				return colMouth // sides of mouth cavity, past tongue edge
			}
			// Median sulcus: a darker groove running down the centre
			if math.Abs(dx) < 0.7 {
				return colTongueGroove
			}
			return colTongue
		}

		return colMouth // dark gap between teeth and tongue
	case bfy < lowerBot:
		return colLip // lower lip
	default:
		return colBg // below mouth
	}
}

// RenderMouth writes a full 240×240 RGB565 frame of the mouth into buf.
func RenderMouth(mp MouthParams, buf []byte) {
	for y := 0; y < screenH; y++ {
		for x := 0; x < screenW; x++ {
			base := mouthBrickBase(x/brickPx, y/brickPx, mp)
			c := brickPixel(base, x%brickPx, y%brickPx)
			hi, lo := c.rgb565()
			i := (y*screenW + x) * 2
			buf[i] = hi
			buf[i+1] = lo
		}
	}
}

// AnimState represents one of the three eye moods.
type AnimState int

const (
	Neutral  AnimState = iota
	Speaking AnimState = iota
	Asleep   AnimState = iota
)

func (s AnimState) String() string {
	return [...]string{"Neutral", "Speaking", "Asleep"}[s]
}

// Animator maintains per-eye animation state and advances it each tick.
//
// All parameters are smoothed so that state transitions look organic rather
// than snapping to a new position.  The blinkOffset staggers the two eye
// blink clocks so both eyes never blink at exactly the same moment.
type Animator struct {
	state   AnimState
	elapsed float64 // seconds elapsed in current state
	p       EyeParams

	// Neutral-state blink timers
	blinkCD    float64 // countdown (s) until next blink begins
	blinkTimer float64 // >0 while a blink is in progress

	// Speaking-state rhythm phase
	beatPhase float64
}

// NewAnimator creates a fresh Animator starting in the Neutral state.
// blinkOffset shifts the blink clock so two eyes are not perfectly in sync.
func NewAnimator(blinkOffset float64) *Animator {
	return &Animator{
		state:   Neutral,
		p:       restingParams(),
		blinkCD: 2.0 + blinkOffset + rand.Float64()*1.5,
	}
}

// SetState requests a transition to state s.
// The animation parameters carry over smoothly; only the clocks are reset.
func (a *Animator) SetState(s AnimState) {
	if s == a.state {
		return
	}
	a.state = s
	a.elapsed = 0
	a.beatPhase = 0
}

// Tick advances the animation by dt seconds and returns the current EyeParams.
func (a *Animator) Tick(dt float64) EyeParams {
	a.elapsed += dt

	switch a.state {

	// ── Neutral ──────────────────────────────────────────────────────────
	//  · Organic, slow gaze drift.
	//  · Random blinks every 2.8–5 s; eyelid snaps to full closure then reopens.
	//  · Iris / pupil sizes and brightness drift back to resting values.
	case Neutral:
		// Smooth return to resting iris / pupil sizes
		a.p.IrisR += (irisRst - a.p.IrisR) * dt * 5
		a.p.PupilR += (pupilRst - a.p.PupilR) * dt * 5
		// Speaking glow decays quickly
		a.p.IrisBright *= math.Pow(0.02, dt) // → 0 in ~0.8 s

		// Slow, lissajous-style gaze drift (looks natural, never repeats)
		a.p.PupilDX = math.Sin(a.elapsed*0.41) * 0.44
		a.p.PupilDY = math.Cos(a.elapsed*0.31) * 0.34

		// Blink logic
		a.blinkCD -= dt
		if a.blinkCD <= 0 {
			// A blink is in progress
			a.blinkTimer += dt
			const halfBlink = 0.13 // seconds per closing/opening half
			switch {
			case a.blinkTimer < halfBlink:
				// Closing phase: top lid closes fully, bottom dips 25%
				frac := a.blinkTimer / halfBlink
				a.p.EyelidT = frac
				a.p.BottomLidT = frac * 0.25
			case a.blinkTimer < 2*halfBlink:
				// Opening phase
				frac := 1 - (a.blinkTimer-halfBlink)/halfBlink
				a.p.EyelidT = frac
				a.p.BottomLidT = frac * 0.25
			default:
				// Blink complete; schedule the next one
				a.blinkTimer = 0
				a.blinkCooldown()
				a.p.EyelidT = 0
				a.p.BottomLidT = 0
			}
		} else {
			// Not blinking: smoothly open both lids in case we just
			// transitioned from Asleep or a Speaking squint.
			a.p.EyelidT *= math.Pow(0.005, dt) // reaches ~0 in ~1 s
			a.p.BottomLidT *= math.Pow(0.005, dt)
		}

	// ── Speaking ─────────────────────────────────────────────────────────
	//  · Multi-harmonic "speech rhythm" drives iris dilation and brightness.
	//  · Pupils dart left-right with sentence stress.
	//  · Slight squint on stressed syllables.
	case Speaking:
		a.beatPhase += dt * 5.5
		// Primary beat + a faster secondary for irregular feel
		beat := math.Sin(a.beatPhase)*0.7 + math.Sin(a.beatPhase*2.1)*0.3

		targetIrisR := 5.0 + beat*0.26
		targetPupilR := 2.7 + math.Abs(beat)*0.18
		targetBright := 0.22 + math.Abs(beat)*0.38

		speed := dt * 8.0
		a.p.IrisR += (targetIrisR - a.p.IrisR) * speed
		a.p.PupilR += (targetPupilR - a.p.PupilR) * speed
		a.p.IrisBright += (targetBright - a.p.IrisBright) * speed

		// Gaze darts with speech stress; Y follows beat, X drifts independently
		a.p.PupilDX = math.Sin(a.elapsed*3.1) * 0.56
		a.p.PupilDY = beat * 0.36

		// Open the bottom lid fully (in case we came from Asleep)
		// then apply stress-squint only to the top lid
		squint := math.Max(0, beat*0.09)
		a.p.EyelidT += (squint - a.p.EyelidT) * dt * 7
		a.p.BottomLidT += (0 - a.p.BottomLidT) * dt * 4

	// ── Asleep ────────────────────────────────────────────────────────────
	//  · Top lid drifts to 0.54, bottom lid to 0.46 – they meet cleanly at centre.
	//  · Iris and pupil contract as consciousness fades.
	//  · Tiny REM-style twitches are visible while the lids are still parting.
	case Asleep:
		a.p.EyelidT += (0.54 - a.p.EyelidT) * dt * 2.2
		a.p.BottomLidT += (0.46 - a.p.BottomLidT) * dt * 1.8
		a.p.IrisR += (2.6 - a.p.IrisR) * dt * 2.5
		a.p.PupilR += (0.9 - a.p.PupilR) * dt * 2.5
		a.p.IrisBright *= math.Pow(0.01, dt) // dim to 0

		// Subtle dream-twitches (only perceptible while lids are still parting)
		a.p.PupilDX = math.Sin(a.elapsed*0.8) * 0.14
		a.p.PupilDY = math.Cos(a.elapsed*0.6) * 0.12
	}

	return a.p
}

// blinkCooldown resets the blink countdown with a random interval.
func (a *Animator) blinkCooldown() {
	a.blinkCD = 2.8 + rand.Float64()*2.2
}

// ╔══════════════════════════════════════════════════════════════════════════╗
// ║  Mouth animation state machine                                            ║
// ╚══════════════════════════════════════════════════════════════════════════╝

// MouthAnimator drives the mouth display independently from the eye animator.
// It reacts to the same AnimState but animates OpenT and SmileT instead.
type MouthAnimator struct {
	state     AnimState
	elapsed   float64
	beatPhase float64
	mp        MouthParams
}

func NewMouthAnimator() *MouthAnimator {
	return &MouthAnimator{
		state: Neutral,
		mp:    restingMouthParams(),
	}
}

func (m *MouthAnimator) SetState(s AnimState) {
	if s == m.state {
		return
	}
	m.state = s
	m.elapsed = 0
	m.beatPhase = 0
}

// Tick advances the mouth animation by dt seconds and returns current MouthParams.
func (m *MouthAnimator) Tick(dt float64) MouthParams {
	m.elapsed += dt

	smooth := func(cur, tgt, speed float64) float64 {
		return cur + (tgt-cur)*math.Min(1.0, dt*speed)
	}

	switch m.state {

	// ── Neutral ───────────────────────────────────────────────────────────
	//  · Lips closed, gentle resting smile.
	case Neutral:
		m.mp.OpenT = smooth(m.mp.OpenT, 0.0, 4.0)
		m.mp.SmileT = smooth(m.mp.SmileT, 0.50, 4.0)

	// ── Speaking ──────────────────────────────────────────────────────────
	//  · Lower jaw bobs with the speech-beat rhythm.
	//  · Smile flexes slightly on stressed syllables.
	//  · Tongue becomes visible on wide-open frames.
	case Speaking:
		m.beatPhase += dt * 5.5
		beat := math.Sin(m.beatPhase)*0.7 + math.Sin(m.beatPhase*2.1)*0.3
		m.mp.OpenT = smooth(m.mp.OpenT, 0.25+math.Abs(beat)*0.55, 9.0)
		m.mp.SmileT = smooth(m.mp.SmileT, 0.40+beat*0.10, 9.0)

	// ── Asleep ────────────────────────────────────────────────────────────
	//  · Jaw slackens very slightly (small snore oscillation).
	//  · Corners of mouth relax into a subtle frown.
	case Asleep:
		snore := math.Sin(m.elapsed*1.8) * 0.03
		m.mp.OpenT = smooth(m.mp.OpenT, 0.04+snore, 1.8)
		m.mp.SmileT = smooth(m.mp.SmileT, -0.18, 1.8)
	}

	return m.mp
}

func initDisplay(stateCh chan AnimState) {
	log.Println("legoeyes: starting")

	if _, err := host.Init(); err != nil {
		log.Fatal("periph host.Init:", err)
	}

	// ── Open both SPI ports (shared bus, separate chip-select lines) ──────
	mustPort := func(name string) spi.PortCloser {
		p, err := spireg.Open(name)
		if err != nil {
			log.Fatalf("spireg.Open(%q): %v", name, err)
		}
		return p
	}
	portL := mustPort("SPI0.0") // left  display (CS0 → GPIO8) — MOUTH
	portR := mustPort("SPI0.1") // right display (CS1 → GPIO7) — EYE
	defer portL.Close()
	defer portR.Close()

	// Connect at 40 MHz, SPI mode 0 (CPOL=0 CPHA=0), 8 bits per word.
	// If you see display glitches try 32 MHz or 20 MHz.
	mustConn := func(p spi.PortCloser, name string) spi.Conn {
		c, err := p.Connect(40*physic.MegaHertz, spi.Mode0, 8)
		if err != nil {
			log.Fatalf("spi connect %s: %v", name, err)
		}
		return c
	}
	connL := mustConn(portL, "SPI0.0")
	connR := mustConn(portR, "SPI0.1")

	// ── GPIO pins ─────────────────────────────────────────────────────────
	mustPin := func(name string) gpio.PinIO {
		p := gpioreg.ByName(name)
		if p == nil {
			log.Fatalf("GPIO pin %q not found – check wiring / permissions", name)
		}
		return p
	}
	dcL := mustPin("GPIO25") // D/C for left  display (mouth)
	dcR := mustPin("GPIO22") // D/C for right display (eye)
	rst := mustPin("GPIO27") // RST (shared – both displays)

	// ── Hardware reset (simultaneous for both displays) ───────────────────
	log.Println("legoeyes: hardware reset")
	if err := rst.Out(gpio.Low); err != nil {
		log.Fatal("RST low:", err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := rst.Out(gpio.High); err != nil {
		log.Fatal("RST high:", err)
	}
	time.Sleep(150 * time.Millisecond)

	// ── Initialise each display ───────────────────────────────────────────
	dispL := newDisplay(connL, dcL)
	dispR := newDisplay(connR, dcR)
	log.Println("legoeyes: init left display (mouth)")
	dispL.Init()
	log.Println("legoeyes: init right display (eye)")
	dispR.Init()
	log.Println("legoeyes: displays ready")

	// ── Pre-allocate frame buffers (reused every frame) ───────────────────
	bufL := make([]byte, frameSize) // mouth frame
	bufR := make([]byte, frameSize) // eye frame

	// ── Animators ─────────────────────────────────────────────────────────
	mouthAnim := NewMouthAnimator() // drives left display
	eyeAnim := NewAnimator(0.0)     // drives right display

	// ── Render / push loop (~30 fps) ──────────────────────────────────────
	ticker := time.NewTicker(time.Second / time.Duration(targetFPS))
	defer ticker.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var curState AnimState
	for {
		select {

		case <-sigCh:
			log.Println("legoeyes: shutting down")
			return

		case s := <-stateCh:
			if s != curState {
				curState = s
				log.Printf("legoeyes: → %s", curState)
				mouthAnim.SetState(s)
				eyeAnim.SetState(s)
			}

		case <-ticker.C:
			mp := mouthAnim.Tick(frameDT)
			ep := eyeAnim.Tick(frameDT)

			RenderMouth(mp, bufL)
			RenderEye(ep, bufR)

			dispL.Flush(bufL)
			dispR.Flush(bufR)
		}
	}
}
