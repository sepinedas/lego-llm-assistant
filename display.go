// Photo-realistic animated eye (SPI0.1) and mouth (SPI0.0)
// for two GC9A01 round 240×240 displays via periph.io in Go.
//
// Wiring (Raspberry Pi):
//   SPI0.0 mouth : CS=GPIO8,  DC=GPIO24, RST=GPIO25
//   SPI0.1 eye   : CS=GPIO7,  DC=GPIO23, RST=GPIO22
//
// go mod tidy && go run main.go

package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
)

const (
	W   = 240
	H   = 240
	CX  = W / 2
	CY  = H / 2
	RAD = 118
)

// ── Math ──────────────────────────────────────────────────────────────────────

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
func lerpF(a, b, t float64) float64 { return a + (b-a)*t }
func smoothstep(t float64) float64  { t = clampF(t, 0, 1); return t * t * (3 - 2*t) }
func sinPI(t float64) float64       { return math.Sin(math.Pi * t) }
func sq(x float64) float64          { return x * x }

// ── Deterministic LCG (same seed → same iris every frame) ────────────────────

type lcg struct{ s uint64 }

func newLCG(seed uint64) *lcg { return &lcg{seed} }
func (l *lcg) next() float64 {
	l.s = l.s*6364136223846793005 + 1442695040888963407
	return float64(l.s>>33) / float64(1<<31)
}

// ── Colour helpers ────────────────────────────────────────────────────────────

func rgba(r, g, b, a int) color.RGBA         { return color.RGBA{uint8(r), uint8(g), uint8(b), uint8(a)} }
func withA(c color.RGBA, a uint8) color.RGBA { c.A = a; return c }

func lerpC(a, b color.RGBA, t float64) color.RGBA {
	t = clampF(t, 0, 1)
	return color.RGBA{
		uint8(lerpF(float64(a.R), float64(b.R), t)),
		uint8(lerpF(float64(a.G), float64(b.G), t)),
		uint8(lerpF(float64(a.B), float64(b.B), t)),
		uint8(lerpF(float64(a.A), float64(b.A), t)),
	}
}

func blendOver(dst, src color.RGBA) color.RGBA {
	if src.A == 0 {
		return dst
	}
	if src.A == 255 {
		return src
	}
	sa := float64(src.A) / 255
	return color.RGBA{
		uint8(lerpF(float64(dst.R), float64(src.R), sa)),
		uint8(lerpF(float64(dst.G), float64(src.G), sa)),
		uint8(lerpF(float64(dst.B), float64(src.B), sa)),
		255,
	}
}

// ── Gradient ──────────────────────────────────────────────────────────────────

type gStop struct {
	pos float64
	c   color.RGBA
}

func evalGrad(stops []gStop, t float64) color.RGBA {
	t = clampF(t, 0, 1)
	for i := 1; i < len(stops); i++ {
		if t <= stops[i].pos {
			s0, s1 := stops[i-1], stops[i]
			span := s1.pos - s0.pos
			if span < 1e-9 {
				return s1.c
			}
			return lerpC(s0.c, s1.c, (t-s0.pos)/span)
		}
	}
	return stops[len(stops)-1].c
}

// ── Canvas ────────────────────────────────────────────────────────────────────

type Canvas struct{ img *image.RGBA }

func newCanvas() *Canvas { return &Canvas{image.NewRGBA(image.Rect(0, 0, W, H))} }

func (c *Canvas) blend(x, y int, src color.RGBA) {
	if x < 0 || x >= W || y < 0 || y >= H {
		return
	}
	c.img.SetRGBA(x, y, blendOver(c.img.RGBAAt(x, y), src))
}

// disc — AA filled circle, radial gradient (stop 0=centre, 1=edge).
func (c *Canvas) disc(cx, cy, r float64, stops []gStop) {
	ir := int(r) + 2
	for dy := -ir; dy <= ir; dy++ {
		for dx := -ir; dx <= ir; dx++ {
			d := math.Sqrt(float64(dx*dx + dy*dy))
			if d >= r+1 {
				continue
			}
			col := evalGrad(stops, d/r)
			if d > r-1 {
				col.A = uint8(float64(col.A) * (r + 1 - d))
			}
			c.blend(int(cx)+dx, int(cy)+dy, col)
		}
	}
}

func (c *Canvas) discSolid(cx, cy, r float64, col color.RGBA) {
	c.disc(cx, cy, r, []gStop{{0, col}, {1, col}})
}

// ellipse2DRadial — fills an ellipse with a 2-D radial gradient centred at gcx,gcy.
func (c *Canvas) ellipse2DRadial(cx, cy float64, rx, ry int, gcx, gcy, gr float64, stops []gStop) {
	for py := int(cy) - ry - 1; py <= int(cy)+ry+1; py++ {
		dy := float64(py) - cy
		xSpan := float64(rx) * math.Sqrt(clampF(1-dy*dy/float64(ry*ry), 0, 1))
		for px := int(cx-xSpan) - 1; px <= int(cx+xSpan)+1; px++ {
			ddx := (float64(px) - cx) / float64(rx)
			ddy := (float64(py) - cy) / float64(ry)
			d2 := ddx*ddx + ddy*ddy
			if d2 > 1.06 {
				continue
			}
			gd := math.Sqrt(sq(float64(px)-gcx)+sq(float64(py)-gcy)) / gr
			col := evalGrad(stops, gd)
			if d2 > 0.88 {
				col.A = uint8(float64(col.A) * (1.06 - d2) / 0.18)
			}
			c.blend(px, py, col)
		}
	}
}

// ellipseLinGrad — fills an ellipse with a vertical linear gradient.
func (c *Canvas) ellipseLinGrad(cx, cy float64, rx, ry int, stops []gStop) {
	for py := int(cy) - ry - 1; py <= int(cy)+ry+1; py++ {
		dy := float64(py) - cy
		xSpan := float64(rx) * math.Sqrt(clampF(1-dy*dy/float64(ry*ry), 0, 1))
		for px := int(cx-xSpan) - 1; px <= int(cx+xSpan)+1; px++ {
			ddx := (float64(px) - cx) / float64(rx)
			ddy := (float64(py) - cy) / float64(ry)
			d2 := ddx*ddx + ddy*ddy
			if d2 > 1.06 {
				continue
			}
			tY := (float64(py) - (cy - float64(ry))) / (2 * float64(ry))
			col := evalGrad(stops, tY)
			if d2 > 0.88 {
				col.A = uint8(float64(col.A) * (1.06 - d2) / 0.18)
			}
			c.blend(px, py, col)
		}
	}
}

// bez2 evaluates a quadratic Bézier.
func bez2(p0, p1, p2 [2]float64, t float64) [2]float64 {
	u := 1 - t
	return [2]float64{u*u*p0[0] + 2*u*t*p1[0] + t*t*p2[0], u*u*p0[1] + 2*u*t*p1[1] + t*t*p2[1]}
}

// bez3 evaluates a cubic Bézier.
func bez3(p0, p1, p2, p3 [2]float64, t float64) [2]float64 {
	u := 1 - t
	return [2]float64{
		u*u*u*p0[0] + 3*u*u*t*p1[0] + 3*u*t*t*p2[0] + t*t*t*p3[0],
		u*u*u*p0[1] + 3*u*u*t*p1[1] + 3*u*t*t*p2[1] + t*t*t*p3[1],
	}
}

// strokeBez2 — tapered quadratic Bézier stroke.
func (c *Canvas) strokeBez2(p0, p1, p2 [2]float64, rBase, rTip float64, col color.RGBA) {
	steps := int(math.Hypot(p2[0]-p0[0], p2[1]-p0[1])*3.5) + 24
	for i := 0; i <= steps; i++ {
		tt := float64(i) / float64(steps)
		p := bez2(p0, p1, p2, tt)
		c.discSolid(p[0], p[1], lerpF(rBase, rTip, tt), col)
	}
}

// strokeBez3 — uniform cubic Bézier stroke.
func (c *Canvas) strokeBez3(p0, p1, p2, p3 [2]float64, r float64, col color.RGBA) {
	steps := int(math.Hypot(p3[0]-p0[0], p3[1]-p0[1])*3.5) + 24
	for i := 0; i <= steps; i++ {
		tt := float64(i) / float64(steps)
		p := bez3(p0, p1, p2, p3, tt)
		c.discSolid(p[0], p[1], r, col)
	}
}

// clipRound zeroes pixels outside the circular bezel.
func (c *Canvas) clipRound() {
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			if sq(float64(x-CX))+sq(float64(y-CY)) > sq(RAD) {
				c.img.SetRGBA(x, y, color.RGBA{})
			}
		}
	}
}

// fillSkinRadial fills the round canvas with a SSS-style radial skin gradient.
func (c *Canvas) fillSkinRadial(gcx, gcy float64, inner, outer color.RGBA) {
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			dx, dy := float64(x-CX), float64(y-CY)
			if sq(dx)+sq(dy) > sq(RAD) {
				continue
			}
			t := clampF(math.Sqrt(sq(float64(x)-gcx)+sq(float64(y)-gcy))/float64(RAD), 0, 1)
			c.img.SetRGBA(x, y, lerpC(inner, outer, t*t*0.82))
		}
	}
}

// skinPores overlays a subtle seeded specular pore-texture.
func (c *Canvas) skinPores(seed uint64, count int) {
	rng := newLCG(seed)
	for i := 0; i < count; i++ {
		px := CX + int((rng.next()-0.5)*226)
		py := CY + int((rng.next()-0.5)*226)
		if sq(float64(px-CX))+sq(float64(py-CY)) > sq(113) {
			continue
		}
		r := uint8(lerpF(160, 225, rng.next()))
		g := uint8(lerpF(130, 185, rng.next()))
		b := uint8(lerpF(90, 155, rng.next()))
		rad := rng.next()*1.7 + 0.3
		c.disc(float64(px), float64(py), rad, []gStop{
			{0, rgba(int(r), int(g), int(b), 10)},
			{1, rgba(int(r), int(g), int(b), 0)},
		})
	}
}

// ── Iris anatomy — seeded once, stable across frames ──────────────────────────

type (
	irisFiber struct{ ang, jit, curv float64 }
	irisCrypt struct{ ang, r, sz float64 }
)

var (
	gFibers [56]irisFiber
	gCrypts [14]irisCrypt
)

func init() {
	rng := newLCG(31337)
	for i := range gFibers {
		gFibers[i] = irisFiber{
			ang:  float64(i) * math.Pi * 2 / float64(len(gFibers)),
			jit:  rng.next(),
			curv: (rng.next() - 0.5) * 0.12,
		}
	}
	for i := range gCrypts {
		gCrypts[i] = irisCrypt{
			ang: rng.next() * math.Pi * 2,
			r:   0.38 + rng.next()*0.28,
			sz:  2.2 + rng.next()*1.8,
		}
	}
}

// ── EYE RENDERER ─────────────────────────────────────────────────────────────

func renderEye(lid, animT float64, zzzMode bool) *image.RGBA {
	cv := newCanvas()

	// SSS skin base
	cv.fillSkinRadial(float64(CX)-8, float64(CY)-18,
		rgba(245, 224, 200, 255), rgba(184, 132, 90, 255))
	cv.skinPores(9999, 180)

	ex, ey := float64(CX), float64(CY)+7
	ERX, ERY := 80, 49

	// Orbital brow-bone shadow
	for y := int(ey) - ERY - 28; y <= int(ey)-ERY+10; y++ {
		tV := clampF(float64(y-(int(ey)-ERY-28))/38.0, 0, 1)
		a := uint8(clampF((1-tV)*tV*4*56, 0, 56))
		for x := int(ex) - ERX - 8; x <= int(ex)+ERX+8; x++ {
			cv.blend(x, y, rgba(90, 50, 25, int(a)))
		}
	}

	// Under-eye soft tissue
	for y := int(ey) + ERY - 4; y <= int(ey)+ERY+22; y++ {
		tV := clampF(float64(y-(int(ey)+ERY-4))/26.0, 0, 1)
		a := uint8(clampF(tV*(1-tV)*4*46, 0, 46))
		for x := int(ex) - ERX; x <= int(ex)+ERX; x++ {
			cv.blend(x, y, rgba(180, 130, 90, int(a)))
		}
	}

	// ── Sclera ──
	cv.ellipse2DRadial(ex, ey, ERX, ERY, ex-14, ey-9, float64(ERX)*1.12, []gStop{
		{0.0, rgba(255, 255, 254, 255)},
		{0.38, rgba(249, 250, 253, 255)},
		{0.72, rgba(234, 236, 245, 255)},
		{0.92, rgba(216, 220, 238, 255)},
		{1.0, rgba(200, 205, 230, 255)},
	})

	// Scleral veins
	rng := newLCG(777)
	for i := 0; i < 8; i++ {
		baseAng := math.Pi*0.06 + float64(i)*math.Pi/8.5
		px, py := ex+math.Cos(baseAng)*float64(ERY)*0.55, ey+math.Sin(baseAng)*float64(ERY)*0.55
		for seg := 0; seg < 6; seg++ {
			jitter := rng.next()*0.06 - 0.03
			a2 := baseAng + float64(seg+1)*0.09 + math.Sin(float64(i)*1.7+float64(seg))*0.06 + jitter
			r1 := float64(ERY)*0.55 + float64(seg+1)*5.2
			nx := ex + math.Cos(a2)*clampF(r1*float64(ERX)/float64(ERY), 0, float64(ERX)-3)
			ny := ey + math.Sin(a2)*clampF(r1, 0, float64(ERY)-2)
			alpha := int(clampF((0.10-float64(seg)*0.012)*255, 0, 255))
			cv.strokeBez2([2]float64{px, py}, [2]float64{(px + nx) / 2, (py + ny) / 2},
				[2]float64{nx, ny}, 0.6-float64(seg)*0.07, 0.2, rgba(195, 78, 78, alpha))
			px, py = nx, ny
		}
	}

	// ── Iris (hazel) ──
	if lid > 0.04 {
		IR := 33.0

		// Deep radial base — hazel brown
		cv.disc(ex, ey, IR, []gStop{
			{0.00, rgba(139, 122, 58, 255)},
			{0.28, rgba(107, 90, 40, 255)},
			{0.55, rgba(74, 62, 24, 255)},
			{0.78, rgba(46, 40, 16, 255)},
			{0.92, rgba(24, 20, 8, 255)},
			{1.00, rgba(8, 5, 2, 255)},
		})

		// Stroma fibers
		for _, f := range gFibers {
			r0 := 5.5 + f.jit*3.8
			r1 := IR - 2.2 - f.jit*2.8
			aEnd := f.ang + f.curv
			// draw fiber as series of dots along quadratic curve
			cMidX := math.Cos((f.ang+aEnd)/2) * (r0 + r1) / 2 * (1 + math.Abs(f.curv)*0.8)
			cMidY := math.Sin((f.ang+aEnd)/2) * (r0 + r1) / 2 * (1 + math.Abs(f.curv)*0.8)
			steps := 22
			for s := 0; s <= steps; s++ {
				tt := float64(s) / float64(steps)
				u := 1 - tt
				fpx := ex + u*u*math.Cos(f.ang)*r0 + 2*u*tt*cMidX + tt*tt*math.Cos(aEnd)*r1
				fpy := ey + u*u*math.Sin(f.ang)*r0 + 2*u*tt*cMidY + tt*tt*math.Sin(aEnd)*r1
				brightness := lerpF(0.12, 0.30, f.jit)
				alpha := lerpF(0, brightness, tt) * lerpF(brightness, 0.08, tt)
				rr := uint8(lerpF(140, 180, f.jit))
				gg := uint8(lerpF(120, 155, f.jit))
				bb := uint8(lerpF(40, 80, f.jit))
				cv.blend(int(fpx), int(fpy), rgba(int(rr), int(gg), int(bb), int(alpha*255)))
			}
		}

		// Collarette rings
		for _, rr := range []struct{ r, a float64 }{{0.60, 90}, {0.72, 46}} {
			steps := 300
			for s := 0; s <= steps; s++ {
				ang := float64(s) / float64(steps) * math.Pi * 2
				cv.blend(int(ex+math.Cos(ang)*IR*rr.r), int(ey+math.Sin(ang)*IR*rr.r),
					rgba(120, 105, 42, int(rr.a)))
			}
		}

		// Crypts
		for _, cr := range gCrypts {
			crx := ex + math.Cos(cr.ang)*IR*cr.r
			cry := ey + math.Sin(cr.ang)*IR*cr.r
			cv.disc(crx, cry, cr.sz, []gStop{
				{0, rgba(8, 6, 2, 132)},
				{1, rgba(8, 6, 2, 0)},
			})
		}

		// Limbal ring — dark annulus
		for s := 0; s <= 400; s++ {
			ang := float64(s) / 400 * math.Pi * 2
			for dr := -1.8; dr <= 0.6; dr += 0.5 {
				cv.blend(int(ex+math.Cos(ang)*(IR+dr)), int(ey+math.Sin(ang)*(IR+dr)),
					rgba(4, 3, 1, 215))
			}
		}

		// Iridescent arc segments
		iridA := []int{100, 80, 100, 80}
		iridC := [][3]int{{200, 180, 80}, {160, 200, 100}, {80, 180, 200}, {180, 120, 200}}
		for i := 0; i < 4; i++ {
			for s := 0; s <= 60; s++ {
				ang := (float64(i) + float64(s)/60.0) * math.Pi / 2
				cv.blend(int(ex+math.Cos(ang)*(IR-1.8)), int(ey+math.Sin(ang)*(IR-1.8)),
					rgba(iridC[i][0], iridC[i][1], iridC[i][2], iridA[i]))
			}
		}

		// Pupil
		cv.disc(ex, ey, 16.5, []gStop{
			{0.00, rgba(34, 24, 58, 255)},
			{0.50, rgba(14, 8, 24, 255)},
			{0.85, rgba(4, 2, 8, 255)},
			{1.00, rgba(0, 0, 2, 255)},
		})

		// ── 3-tier corneal specular ──
		// Large soft environmental
		cv.disc(ex+9, ey-12, 10, []gStop{
			{0.00, rgba(255, 255, 255, 0)},
			{0.15, rgba(255, 255, 255, 140)},
			{0.55, rgba(255, 255, 255, 56)},
			{1.00, rgba(255, 255, 255, 0)},
		})
		// Sharp primary
		cv.discSolid(ex+12, ey-14, 3.8, rgba(255, 255, 255, 242))
		// Secondary
		cv.discSolid(ex-9, ey-15, 1.9, rgba(255, 255, 255, 158))
		// Micro accent
		cv.discSolid(ex+15, ey-9, 0.9, rgba(255, 255, 255, 97))

		// Sub-corneal haze ring
		for s := 0; s <= 300; s++ {
			ang := float64(s) / 300 * math.Pi * 2
			cv.blend(int(ex+math.Cos(ang)*IR*0.83), int(ey+math.Sin(ang)*IR*0.83),
				rgba(210, 205, 180, 46))
		}

		// Corneal wet sheen (diagonal highlight across globe)
		cv.ellipse2DRadial(ex, ey, ERX, ERY, ex-12, ey-14, float64(ERX)*1.4, []gStop{
			{0.0, rgba(230, 240, 255, 22)},
			{0.4, rgba(230, 240, 255, 9)},
			{1.0, rgba(230, 240, 255, 0)},
		})
	}

	// Tear meniscus
	for s := 0; s <= 220; s++ {
		ang := lerpF(0.07, 0.93, float64(s)/220) * math.Pi
		fpx := ex + float64(ERX)*math.Cos(ang)
		fpy := ey + (float64(ERY)-0.5)*math.Sin(ang) + 1.8
		cv.disc(fpx, fpy, 1.5, []gStop{
			{0, rgba(175, 210, 240, 155)},
			{1, rgba(175, 210, 240, 0)},
		})
	}

	// Lacrimal caruncle
	cv.disc(ex-float64(ERX)+11, ey, 11, []gStop{
		{0.0, rgba(232, 125, 140, 235)},
		{0.55, rgba(228, 115, 132, 112)},
		{1.0, rgba(228, 115, 132, 0)},
	})

	// ── Upper eyelid ──
	lidEdgeY := ey - float64(ERY) + 2*float64(ERY)*(1-lid)

	// Cast shadow onto eyeball
	if lid < 0.94 {
		for y := int(lidEdgeY); y <= int(lidEdgeY)+22; y++ {
			dy := float64(y) - ey
			xSpan := float64(ERX) * math.Sqrt(clampF(1-dy*dy/float64(ERY*ERY), 0, 1))
			t := clampF(float64(y-int(lidEdgeY))/22, 0, 1)
			a := uint8(clampF((1-t)*(1-t)*122, 0, 122))
			for x := int(ex - xSpan); x <= int(ex+xSpan); x++ {
				cv.blend(x, y, rgba(30, 12, 5, int(a)))
			}
		}
	}

	// Skin lid fill
	for y := int(ey) - ERY - 5; y <= int(lidEdgeY)+1; y++ {
		dy := float64(y) - ey
		var xSpan float64
		if math.Abs(dy) <= float64(ERY) {
			xSpan = float64(ERX) * math.Sqrt(clampF(1-dy*dy/float64(ERY*ERY), 0, 1))
		} else {
			xSpan = float64(ERX) + 2
		}
		tV := clampF((float64(y)-(ey-float64(ERY)-5))/(float64(ERY)+6), 0, 1)
		col := evalGrad([]gStop{
			{0.0, rgba(242, 210, 174, 255)},
			{0.28, rgba(237, 200, 162, 255)},
			{0.62, rgba(224, 176, 128, 255)},
			{0.88, rgba(200, 136, 85, 255)},
			{1.00, rgba(176, 112, 64, 255)},
		}, tV)
		for x := int(ex - xSpan); x <= int(ex+xSpan); x++ {
			cv.blend(x, y, col)
		}
	}

	// Lid margin warmth
	for off := 0; off <= 8; off++ {
		y := int(lidEdgeY) + off + 1
		dy := float64(y) - ey
		xSpan := float64(ERX) * math.Sqrt(clampF(1-dy*dy/float64(ERY*ERY), 0, 1))
		t := clampF(float64(off)/8, 0, 1)
		a := uint8(clampF((1-t)*(1-t)*107, 0, 107))
		for x := int(ex - xSpan); x <= int(ex+xSpan); x++ {
			cv.blend(x, y, rgba(208, 145, 95, int(a)))
		}
	}

	// Eyelid crease
	if lid > 0.44 {
		fa := smoothstep(clampF((lid-0.44)/0.38, 0, 1))
		cY := ey - float64(ERY)*0.60
		p0 := [2]float64{ex - float64(ERX)*0.70, cY + 8}
		p1 := [2]float64{ex, cY - 4}
		p2 := [2]float64{ex + float64(ERX)*0.70, cY + 8}
		for s := 0; s <= 240; s++ {
			tt := float64(s) / 240
			p := bez2(p0, p1, p2, tt)
			str := sinPI(tt)
			cv.discSolid(p[0], p[1], 1.0, rgba(138, 85, 53, int(fa*str*106)))
			cv.discSolid(p[0], p[1]-2, 0.8, rgba(240, 216, 178, int(fa*str*50)))
		}
	}

	// Lower lid fleshy rim
	for s := 0; s <= 240; s++ {
		ang := lerpF(0.04, 0.96, float64(s)/240) * math.Pi
		fpx := ex + (float64(ERX)+2)*math.Cos(ang)
		fpy := ey + (float64(ERY)+2)*math.Sin(ang)
		cv.disc(fpx, fpy+6, 7, []gStop{
			{0.0, rgba(242, 212, 172, 0)},
			{0.45, rgba(242, 212, 172, 235)},
			{1.0, rgba(228, 195, 150, 240)},
		})
	}
	// Pink conjunctival rim
	for s := 0; s <= 220; s++ {
		ang := lerpF(0.06, 0.94, float64(s)/220) * math.Pi
		fpx := ex + float64(ERX)*math.Cos(ang)
		fpy := ey + (float64(ERY)+2)*math.Sin(ang) + 1
		cv.discSolid(fpx, fpy, 1.3, rgba(210, 108, 118, 122))
	}

	// ── Lash lines ──
	for s := 0; s <= 460; s++ {
		ang := lerpF(math.Pi*1.08, -0.02, float64(s)/460)
		cv.discSolid(ex+float64(ERX)*math.Cos(ang), ey+float64(ERY)*math.Sin(ang), 2.3, rgba(14, 8, 26, 255))
	}
	for s := 0; s <= 250; s++ {
		ang := lerpF(0.05, 0.95, float64(s)/250) * math.Pi
		cv.discSolid(ex+(float64(ERX)+1)*math.Cos(ang), ey+(float64(ERY)+1)*math.Sin(ang), 1.0, rgba(35, 18, 50, 200))
	}

	// ── Individual eyelashes ──
	if lid > 0.06 {
		type lDef struct{ ang, length, fan, baseW float64 }
		lashes := []lDef{
			{208, 22, -19, 2.8},
			{221, 27, -16, 3.2},
			{234, 31, -13, 3.4},
			{248, 30, -9, 3.3},
			{263, 28, -6, 3.1},
			{278, 30, -9, 3.3},
			{292, 31, -13, 3.4},
			{305, 27, -16, 3.2},
			{318, 22, -19, 2.8},
		}
		for _, l := range lashes {
			rad := l.ang * math.Pi / 180
			bx := ex + float64(ERX)*math.Cos(rad)
			by := ey + float64(ERY)*math.Sin(rad)*0.86
			fanRad := (l.ang + l.fan) * math.Pi / 180
			ex2 := bx + l.length*math.Cos(fanRad)
			ey2 := by + l.length*math.Sin(fanRad) - 6
			cpx := (bx+ex2)/2 + 9*math.Cos(fanRad+math.Pi/2.3)
			cpy := (by+ey2)/2 + 9*math.Sin(fanRad+math.Pi/2.3)
			for s := 0; s <= 32; s++ {
				tt := float64(s) / 32
				u := 1 - tt
				px := u*u*bx + 2*u*tt*cpx + tt*tt*ex2
				py := u*u*by + 2*u*tt*cpy + tt*tt*ey2
				r := l.baseW * (1 - tt*0.84)
				cv.discSolid(px, py, r, rgba(10, 5, 22, int((1-tt*0.28)*255)))
			}
			cv.discSolid(bx, by, 3.4, rgba(8, 3, 18, 153))
		}
		// Lower lashes
		for i := 0; i < 9; i++ {
			ang := (17.0 + float64(i)*19) * math.Pi / 180
			bx := ex + float64(ERX)*math.Cos(ang)
			by := ey + float64(ERY)*math.Sin(ang) + 1
			mid := [2]float64{(bx + bx + 8*math.Cos(ang+0.16)) / 2, (by + by + 8*math.Sin(ang+0.16)) / 2}
			cv.strokeBez2([2]float64{bx, by}, mid, [2]float64{bx + 8*math.Cos(ang+0.16), by + 8*math.Sin(ang+0.16)}, 0.9, 0.2, rgba(26, 14, 36, 158))
		}
	}

	// ── Eyebrow — microbladed hair strands ──
	{
		browY := ey - float64(ERY) - 16
		bL := [2]float64{ex - 72, browY + 5}
		bC1 := [2]float64{ex - 22, browY - 18}
		bC2 := [2]float64{ex + 16, browY - 18}
		bR := [2]float64{ex + 72, browY + 10}
		browRng := newLCG(4242)
		for s := 0; s <= 290; s++ {
			tt := float64(s) / 290
			p := bez3(bL, bC1, bC2, bR, tt)
			thick := 5.8 * sinPI(tt) * (1 - 0.28*math.Abs(tt-0.44))
			if thick < 0.4 {
				thick = 0.4
			}
			alpha := clampF(sinPI(tt)*1.38, 0, 1)
			nHairs := int(thick*1.3) + 1
			for h := 0; h < nHairs; h++ {
				offset := (float64(h)/float64(max(nHairs-1, 1)) - 0.5) * thick * 1.55
				hAlpha := alpha * (1 - math.Abs(float64(h)/float64(max(nHairs-1, 1))-0.5)*1.8)
				hAlpha = clampF(hAlpha, 0, 1)
				jitR := browRng.next()*8 + 24
				jitG := browRng.next()*8 + 14
				jitB := browRng.next()*6 + 8
				cv.blend(int(p[0]), int(p[1]+offset), rgba(int(jitR), int(jitG), int(jitB), int(hAlpha*210)))
			}
			cv.blend(int(p[0]), int(p[1]-thick/2-1), rgba(245, 215, 175, int(clampF(alpha*0.18, 0, 0.18)*255)))
		}
	}

	// ── ZZZ ──
	if zzzMode {
		drawZZZ(cv, ex+46, ey-26, animT)
	}

	cv.clipRound()
	return cv.img
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func drawZZZ(cv *Canvas, x, baseY, t float64) {
	for i := 0; i < 3; i++ {
		sz := 20.0 - float64(i)*4
		drift := math.Mod(t*14+float64(i)*9, 28)
		zy := baseY - drift - float64(i)*21
		a := uint8(clampF((1-drift/28)*(1-float64(i)*0.23)*195, 0, 195))
		col := rgba(100, 82, 195, int(a))
		xi, yi, s := int(x+float64(i)*22), int(zy), int(sz)
		for j := 0; j <= s; j++ {
			cv.blend(xi+j, yi, col)
			cv.blend(xi+j, yi+1, col)
		}
		for j := 0; j <= s; j++ {
			cv.blend(xi+s-j, yi+2+j, col)
			cv.blend(xi+s-j-1, yi+2+j, col)
		}
		for j := 0; j <= s; j++ {
			cv.blend(xi+j, yi+s+2, col)
			cv.blend(xi+j, yi+s+3, col)
		}
	}
}

// ── MOUTH RENDERER ────────────────────────────────────────────────────────────

func renderMouth(smile float64, asleep bool, animT float64) *image.RGBA {
	cv := newCanvas()
	// SSS skin, no nose anatomy
	cv.fillSkinRadial(float64(CX)-6, float64(CY)-14,
		rgba(244, 214, 186, 255), rgba(180, 120, 72, 255))
	cv.skinPores(55555, 160)

	mx, my := float64(CX), float64(CY)+12

	// Nasolabial fold crease (lower half only — no nose)
	foldA := int(clampF((0.08+smile*0.15)*255, 0, 255))
	cv.strokeBez3([2]float64{mx - 44, my - 18}, [2]float64{mx - 56, my}, [2]float64{mx - 62, my + 14}, [2]float64{mx - 58, my + 26}, 2.0, rgba(160, 112, 64, foldA))
	cv.strokeBez3([2]float64{mx + 44, my - 18}, [2]float64{mx + 56, my}, [2]float64{mx + 62, my + 14}, [2]float64{mx + 58, my + 26}, 2.0, rgba(160, 112, 64, foldA))

	if asleep {
		renderAsleepMouth(cv, mx, my)
		cv.clipRound()
		return cv.img
	}

	mW := 66.0
	cL := smile * 22
	bD := smile * 16
	oH := 26.0 + smile*28

	Lx, Rx := mx-mW, mx+mW
	Lcy := my + cL
	bowMid := [2]float64{mx, my + bD + 2}
	peakL := [2]float64{mx - mW*0.40, my - 13 + bD}
	peakR := [2]float64{mx + mW*0.40, my - 13 + bD}
	lC1 := [2]float64{mx - mW*0.58, my + oH + 4}
	lC2 := [2]float64{mx + mW*0.58, my + oH + 4}

	// Sample bezier boundaries
	upperY := make([]float64, W)
	lowerY := make([]float64, W)
	for i := range upperY {
		upperY[i] = 1e9
	}
	for i := range lowerY {
		lowerY[i] = -1e9
	}
	sampleU := func(p0, p1, p2 [2]float64) {
		for i := 0; i <= 700; i++ {
			p := bez2(p0, p1, p2, float64(i)/700)
			xi := int(math.Round(p[0]))
			if xi >= 0 && xi < W && p[1] < upperY[xi] {
				upperY[xi] = p[1]
			}
		}
	}
	sampleL := func(p0, p1, p2, p3 [2]float64) {
		for i := 0; i <= 700; i++ {
			p := bez3(p0, p1, p2, p3, float64(i)/700)
			xi := int(math.Round(p[0]))
			if xi >= 0 && xi < W && p[1] > lowerY[xi] {
				lowerY[xi] = p[1]
			}
		}
	}
	sampleU([2]float64{Lx, Lcy}, peakL, bowMid)
	sampleU(bowMid, peakR, [2]float64{Rx, Lcy})
	sampleL([2]float64{Lx, Lcy}, lC1, lC2, [2]float64{Rx, Lcy})
	for x := int(Lx); x <= int(Rx); x++ {
		if upperY[x] > 1e8 {
			upperY[x] = my
		}
		if lowerY[x] < -1e8 {
			lowerY[x] = my + oH + 8
		}
	}

	// Dark oral cavity
	for x := int(Lx); x <= int(Rx); x++ {
		uy, ly := upperY[x], lowerY[x]
		if ly <= uy {
			continue
		}
		span := ly - uy
		for y := int(uy); y <= int(ly); y++ {
			tt := float64(y-int(uy)) / span
			dx := math.Abs(float64(x)-mx) / mW
			cv.img.SetRGBA(x, y, evalGrad([]gStop{
				{0, rgba(6, 2, 4, 255)},
				{0.45, rgba(18, 8, 14, 255)},
				{1, rgba(32, 12, 20, 255)},
			}, clampF(dx*0.55+tt*0.45, 0, 1)))
		}
	}

	// Teeth
	toothH := oH*0.50 + 2
	for x := int(Lx) + 8; x <= int(Rx)-8; x++ {
		if upperY[x] > 1e8 {
			continue
		}
		tTop := upperY[x] + 5
		tBot := math.Min(tTop+toothH, lowerY[x]-1)
		for y := int(tTop); y <= int(tBot); y++ {
			tt := clampF(float64(y-int(tTop))/toothH, 0, 1)
			col := evalGrad([]gStop{
				{0.0, rgba(248, 248, 246, 255)},
				{0.35, rgba(242, 242, 238, 255)},
				{1.0, rgba(218, 215, 208, 255)},
			}, tt)
			cv.img.SetRGBA(x, y, col)
			if float64(y) < tTop+toothH*0.28 {
				cv.blend(x, y, rgba(255, 255, 255, 76))
			}
		}
		// gum
		for y := int(upperY[x]) + 4; y <= int(tTop)+1; y++ {
			t := clampF(float64(y-int(upperY[x])-4)/7, 0, 1)
			cv.blend(x, y, rgba(212, 118, 132, int((1-t)*148)))
		}
		// tooth shadow
		tBotI := int(tTop + toothH)
		for off := -4; off <= 5; off++ {
			t := clampF(float64(off+4)/9, 0, 1)
			cv.blend(x, tBotI+off, rgba(0, 0, 0, int(t*t*65)))
		}
	}
	// Interdental
	for _, ox := range []float64{-24, 0, 24} {
		xi := int(mx + ox)
		if upperY[xi] > 1e8 {
			continue
		}
		for y := int(upperY[xi] + 5); y <= int(upperY[xi]+5+toothH*0.70); y++ {
			t := clampF(float64(y-int(upperY[xi]+5))/(toothH*0.70), 0, 1)
			cv.blend(xi, y, rgba(75, 68, 58, int((1-t)*130)))
			cv.blend(xi+1, y, rgba(75, 68, 58, int((1-t)*55)))
		}
	}

	// ── Upper lip ──
	drawUpperLip(cv, [2]float64{Lx, Lcy}, peakL, bowMid, peakR, [2]float64{Rx, Lcy})

	// ── Lower lip ──
	drawLowerLip(cv, [2]float64{Lx, Lcy}, lC1, lC2, [2]float64{Rx, Lcy}, oH)

	// Vermillion border
	vC := rgba(138, 34, 56, 204)
	cv.strokeBez2([2]float64{Lx, Lcy}, peakL, bowMid, 1.8, 1.3, vC)
	cv.strokeBez2(bowMid, peakR, [2]float64{Rx, Lcy}, 1.3, 1.8, vC)
	cv.strokeBez3([2]float64{Lx, Lcy}, lC1, lC2, [2]float64{Rx, Lcy}, 1.5, rgba(128, 28, 50, 185))

	// Parting line from boundary samples
	for x := int(Lx) + 2; x <= int(Rx)-2; x++ {
		if upperY[x] < 1e8 {
			cv.blend(x, int(upperY[x]), rgba(108, 22, 42, 210))
		}
	}

	// Commissure creases
	cv.strokeBez2([2]float64{Lx, Lcy - 4}, [2]float64{Lx - 3, Lcy + 1}, [2]float64{Lx - 5, Lcy + 8}, 1.0, 0.4, rgba(98, 28, 46, 135))
	cv.strokeBez2([2]float64{Rx, Lcy - 4}, [2]float64{Rx + 3, Lcy + 1}, [2]float64{Rx + 5, Lcy + 8}, 1.0, 0.4, rgba(98, 28, 46, 135))

	// Dimples
	if smile > 0.28 {
		da := smoothstep(clampF((smile-0.28)/0.72, 0, 1))
		cv.disc(Lx+6, Lcy, 11, []gStop{{0, rgba(122, 38, 58, int(da*148))}, {1, rgba(122, 38, 58, 0)}})
		cv.disc(Rx-6, Lcy, 11, []gStop{{0, rgba(122, 38, 58, int(da*148))}, {1, rgba(122, 38, 58, 0)}})
	}

	cv.clipRound()
	return cv.img
}

func drawUpperLip(cv *Canvas, l, pL, mid, pR, r [2]float64) {
	for _, half := range [][3][2]float64{{l, pL, mid}, {mid, pR, r}} {
		for s := 0; s <= 220; s++ {
			tt := float64(s) / 220
			p := bez2(half[0], half[1], half[2], tt)
			col := evalGrad([]gStop{
				{0.0, rgba(168, 52, 84, 255)},
				{0.5, rgba(196, 80, 108, 255)},
				{1.0, rgba(168, 52, 84, 255)},
			}, tt)
			cv.discSolid(p[0], p[1], 5.2, col)
			ha := uint8(clampF(sinPI(tt)*65, 0, 65))
			cv.discSolid(p[0], p[1]-3.4, 1.5, rgba(255, 192, 208, int(ha)))
		}
	}
}

func drawLowerLip(cv *Canvas, p0, c1, c2, p3 [2]float64, oH float64) {
	for s := 0; s <= 250; s++ {
		tt := float64(s) / 250
		p := bez3(p0, c1, c2, p3, tt)
		tC := sinPI(tt)
		thick := 13.5 * (0.68 + 0.32*tC)
		col := evalGrad([]gStop{
			{0.0, rgba(184, 52, 80, 255)},
			{0.38, rgba(208, 76, 100, 255)},
			{0.62, rgba(224, 94, 114, 255)},
			{1.0, rgba(184, 52, 80, 255)},
		}, tt)
		cv.discSolid(p[0], p[1]+thick*0.32, thick/2, col)
		ga := uint8(clampF(tC*112, 0, 112))
		cv.discSolid(p[0], p[1]+thick*0.74, 2.2, rgba(255, 215, 228, int(ga)))
	}
}

func renderAsleepMouth(cv *Canvas, mx, my float64) {
	restY := my + 4
	// Lower lip body
	cv.strokeBez3([2]float64{mx - 38, restY + 2}, [2]float64{mx - 18, restY + 14}, [2]float64{mx + 18, restY + 14}, [2]float64{mx + 38, restY + 2}, 6.0, rgba(200, 76, 100, 225))
	// Upper lip
	cv.strokeBez3([2]float64{mx - 38, restY + 2}, [2]float64{mx - 18, restY - 10}, [2]float64{mx + 18, restY - 10}, [2]float64{mx + 38, restY + 2}, 5.2, rgba(188, 62, 86, 220))
	// Parting
	cv.strokeBez3([2]float64{mx - 38, restY + 2}, [2]float64{mx - 16, restY - 0.5}, [2]float64{mx + 16, restY - 0.5}, [2]float64{mx + 38, restY + 2}, 0.9, rgba(110, 28, 48, 188))
	// Gloss
	cv.disc(mx, restY+11, 22, []gStop{
		{0.0, rgba(255, 212, 222, 115)},
		{0.55, rgba(255, 212, 222, 45)},
		{1.0, rgba(255, 212, 222, 0)},
	})
	// Corner shadows
	cv.disc(mx-37, restY+2, 4.2, []gStop{{0, rgba(108, 34, 52, 112)}, {1, rgba(108, 34, 52, 0)}})
	cv.disc(mx+37, restY+2, 4.2, []gStop{{0, rgba(108, 34, 52, 112)}, {1, rgba(108, 34, 52, 0)}})
}

// ── Spring physics ────────────────────────────────────────────────────────────

type Spring struct{ pos, vel, k, d float64 }

func newSpring(v, k, d float64) *Spring { return &Spring{pos: v, k: k, d: d} }
func (s *Spring) tick(tgt, dt float64) {
	f := (tgt-s.pos)*s.k - s.vel*s.d
	s.vel += f * dt
	s.pos = clampF(s.pos+s.vel*dt, 0, 1)
}

// ── GC9A01 SPI driver ─────────────────────────────────────────────────────────

type GC9A01 struct {
	conn    spi.Conn
	dc, rst gpio.PinOut
}

func newGC9A01(conn spi.Conn, dc, rst gpio.PinOut) (*GC9A01, error) {
	d := &GC9A01{conn, dc, rst}
	return d, d.init()
}
func (d *GC9A01) cmd(c byte) error    { d.dc.Out(gpio.Low); return d.conn.Tx([]byte{c}, nil) }
func (d *GC9A01) dat(b ...byte) error { d.dc.Out(gpio.High); return d.conn.Tx(b, nil) }
func (d *GC9A01) init() error {
	if d.rst != nil {
		d.rst.Out(gpio.Low)
		time.Sleep(12 * time.Millisecond)
		d.rst.Out(gpio.High)
		time.Sleep(130 * time.Millisecond)
	}
	type cr struct {
		c byte
		b []byte
		w time.Duration
	}
	for _, s := range []cr{
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
		{0xB6, []byte{0x00, 0x00}, 0},
		{0x36, []byte{0x48}, 0},
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
		{0x35, nil, 0},
		{0x21, nil, 0},
		{0x11, nil, 120 * time.Millisecond},
		{0x29, nil, 20 * time.Millisecond},
	} {
		if err := d.cmd(s.c); err != nil {
			return fmt.Errorf("cmd %02X: %w", s.c, err)
		}
		if len(s.b) > 0 {
			if err := d.dat(s.b...); err != nil {
				return fmt.Errorf("dat %02X: %w", s.c, err)
			}
		}
		if s.w > 0 {
			time.Sleep(s.w)
		}
	}
	return nil
}

func (d *GC9A01) draw(img *image.RGBA) error {
	d.cmd(0x2A)
	d.dat(0, 0, 0, W-1)
	d.cmd(0x2B)
	d.dat(0, 0, 0, H-1)
	d.cmd(0x2C)
	buf := make([]byte, W*H*2)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			c := img.RGBAAt(x, y)
			px := (uint16(c.R>>3) << 11) | (uint16(c.G>>2) << 5) | uint16(c.B>>3)
			i := (y*W + x) * 2
			buf[i] = byte(px >> 8)
			buf[i+1] = byte(px)
		}
	}
	d.dc.Out(gpio.High)
	for off := 0; off < len(buf); off += 4096 {
		end := off + 4096
		if end > len(buf) {
			end = len(buf)
		}
		if err := d.conn.Tx(buf[off:end], nil); err != nil {
			return err
		}
	}
	return nil
}

type Display struct {
	dev *GC9A01
	bus spi.PortCloser
}

func openDisplay(busName, dcPin, rstPin string) (*Display, error) {
	b, err := spireg.Open(busName)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", busName, err)
	}
	conn, err := b.Connect(40*physic.MegaHertz, spi.Mode0, 8)
	if err != nil {
		b.Close()
		return nil, fmt.Errorf("connect %s: %w", busName, err)
	}
	dc := gpioreg.ByName(dcPin)
	if dc == nil {
		b.Close()
		return nil, fmt.Errorf("pin %s", dcPin)
	}
	var rst gpio.PinOut
	if p := gpioreg.ByName(rstPin); p != nil {
		rst = p
	}
	dev, err := newGC9A01(conn, dc, rst)
	if err != nil {
		b.Close()
		return nil, err
	}
	return &Display{dev, b}, nil
}
func (d *Display) Close() { d.bus.Close() }

// ── Animation sequence ────────────────────────────────────────────────────────

type phase struct {
	name        string
	dur         time.Duration
	lidTarget   float64
	smileTarget float64
	zzz, asleep bool
}

var sequence = []phase{
	{"awake — smiling", 3600 * time.Millisecond, 1.00, 0.78, false, false},
	{"awake — neutral", 1800 * time.Millisecond, 1.00, 0.06, false, false},
	{"blink", 200 * time.Millisecond, 0.00, 0.06, false, false},
	{"open", 125 * time.Millisecond, 1.00, 0.06, false, false},
	{"smiling", 2600 * time.Millisecond, 1.00, 0.78, false, false},
	{"slow blink", 420 * time.Millisecond, 0.04, 0.62, false, false},
	{"open", 240 * time.Millisecond, 1.00, 0.62, false, false},
	{"smiling wide", 2200 * time.Millisecond, 1.00, 0.82, false, false},
	{"drooping", 1250 * time.Millisecond, 0.10, 0.03, false, false},
	{"asleep", 4500 * time.Millisecond, 0.02, 0.00, true, true},
	{"stirring", 850 * time.Millisecond, 0.50, 0.08, false, true},
	{"waking", 520 * time.Millisecond, 1.00, 0.20, false, false},
}
