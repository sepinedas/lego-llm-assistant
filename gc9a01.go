package main

// gc9a01.go — GC9A01 240×240 circular LCD driver via periph.io SPI.
//
// Wiring (both displays share the same SPI0 bus and RST line):
//
//   Signal    Left (SPI0.0)   Right (SPI0.1)   RPi header pin
//   -------   -------------   --------------   ---------------
//   SCLK      GPIO11          GPIO11           23
//   MOSI      GPIO10          GPIO10           19
//   CS        GPIO8  (CE0)    GPIO7  (CE1)     24 / 26
//   DC        GPIO24          GPIO23           18 / 16
//   RST       GPIO25          GPIO25           22  (shared)
//   BL        GPIO18          GPIO13           12 / 33
//   3.3V / GND as usual

import (
	"fmt"
	"image"
	"image/color"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/spi"
)

// Display geometry.
const (
	DisplayW = 240
	DisplayH = 240
)

// GC9A01 commands.
const (
	cmdSLPOUT = 0x11 // Sleep Out
	cmdINVON  = 0x21 // Display Inversion On
	cmdDISPON = 0x29 // Display On
	cmdCASET  = 0x2A // Column Address Set
	cmdRASET  = 0x2B // Row Address Set
	cmdRAMWR  = 0x2C // Memory Write
	cmdMADCTL = 0x36 // Memory Access Control
	cmdCOLMOD = 0x3A // Interface Pixel Format (color mode)
	cmdTEON   = 0x35 // Tearing Effect Line On
)

// spiChunkRows is how many pixel rows to send in a single SPI Tx call.
// Chunking avoids single transfers that exceed kernel DMA limits (~65 kB).
const spiChunkRows = 10

// GC9A01 wraps a GC9A01 display over a periph.io SPI connection.
type GC9A01 struct {
	conn spi.Conn
	dc   gpio.PinOut // Data / Command select (high = data, low = command)
	rst  gpio.PinOut // Hardware reset (active low)
	bl   gpio.PinOut // Backlight enable (active high), may be nil
}

// NewGC9A01 creates a new driver instance. bl may be nil if the backlight is
// always on or controlled externally.
func NewGC9A01(conn spi.Conn, dc, rst, bl gpio.PinOut) *GC9A01 {
	return &GC9A01{conn: conn, dc: dc, rst: rst, bl: bl}
}

// Init performs a hardware reset followed by the full register-init sequence.
func (d *GC9A01) Init() error {
	// ── Hardware reset ──────────────────────────────────────────────────────
	if err := d.rst.Out(gpio.Low); err != nil {
		return fmt.Errorf("gc9a01 reset low: %w", err)
	}
	time.Sleep(15 * time.Millisecond)
	if err := d.rst.Out(gpio.High); err != nil {
		return fmt.Errorf("gc9a01 reset high: %w", err)
	}
	time.Sleep(120 * time.Millisecond)

	// ── Register initialisation sequence ────────────────────────────────────
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
		{0xB6, []byte{0x00, 0x20}, 0}, // Display Function Control
		{cmdMADCTL, []byte{0x08}, 0},  // Memory Access Control (BGR)
		{cmdCOLMOD, []byte{0x05}, 0},  // 16-bit colour (RGB565)
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
		{0x62, []byte{
			0x18, 0x0D, 0x71, 0xED, 0x70, 0x70,
			0x18, 0x0F, 0x71, 0xEF, 0x70, 0x70,
		}, 0},
		{0x63, []byte{
			0x18, 0x11, 0x71, 0xF1, 0x70, 0x70,
			0x18, 0x13, 0x71, 0xF3, 0x70, 0x70,
		}, 0},
		{0x64, []byte{0x28, 0x29, 0xF1, 0x01, 0xF1, 0x00, 0x07}, 0},
		{0x66, []byte{0x3C, 0x00, 0xCD, 0x67, 0x45, 0x45, 0x10, 0x00, 0x00, 0x00}, 0},
		{0x67, []byte{0x00, 0x3C, 0x00, 0x00, 0x00, 0x01, 0x54, 0x10, 0x32, 0x98}, 0},
		{0x74, []byte{0x10, 0x85, 0x80, 0x00, 0x00, 0x4E, 0x00}, 0},
		{0x98, []byte{0x3E, 0x07}, 0},
		{cmdTEON, nil, 0},
		{cmdINVON, nil, 0},
		{cmdSLPOUT, nil, 120 * time.Millisecond},
		{cmdDISPON, nil, 20 * time.Millisecond},
	}

	for _, s := range seq {
		if err := d.writeCmd(s.cmd); err != nil {
			return fmt.Errorf("gc9a01 init cmd 0x%02X: %w", s.cmd, err)
		}
		if len(s.data) > 0 {
			if err := d.writeData(s.data); err != nil {
				return fmt.Errorf("gc9a01 init data 0x%02X: %w", s.cmd, err)
			}
		}
		if s.wait > 0 {
			time.Sleep(s.wait)
		}
	}

	// Enable backlight.
	if d.bl != nil {
		if err := d.bl.Out(gpio.High); err != nil {
			return fmt.Errorf("gc9a01 backlight: %w", err)
		}
	}
	return nil
}

// writeCmd sends a single command byte (DC low).
func (d *GC9A01) writeCmd(cmd byte) error {
	if err := d.dc.Out(gpio.Low); err != nil {
		return err
	}
	return d.conn.Tx([]byte{cmd}, nil)
}

// writeData sends data bytes (DC high).
func (d *GC9A01) writeData(data []byte) error {
	if err := d.dc.Out(gpio.High); err != nil {
		return err
	}
	return d.conn.Tx(data, nil)
}

// SetWindow sets the pixel write window (column/row address set + RAMWR).
func (d *GC9A01) SetWindow(x0, y0, x1, y1 uint16) error {
	if err := d.writeCmd(cmdCASET); err != nil {
		return err
	}
	if err := d.writeData([]byte{byte(x0 >> 8), byte(x0), byte(x1 >> 8), byte(x1)}); err != nil {
		return err
	}
	if err := d.writeCmd(cmdRASET); err != nil {
		return err
	}
	if err := d.writeData([]byte{byte(y0 >> 8), byte(y0), byte(y1 >> 8), byte(y1)}); err != nil {
		return err
	}
	return d.writeCmd(cmdRAMWR)
}

// DisplayImage renders an image.Image to the full display in RGB565 format.
// The image is expected to be DisplayW × DisplayH pixels; it is cropped or
// padded to fit if necessary.
func (d *GC9A01) DisplayImage(img image.Image) error {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	if w > DisplayW {
		w = DisplayW
	}
	if h > DisplayH {
		h = DisplayH
	}

	if err := d.SetWindow(0, 0, uint16(DisplayW-1), uint16(DisplayH-1)); err != nil {
		return err
	}

	// Build RGB565 row buffer and send in chunks.
	rowBuf := make([]byte, DisplayW*2)
	chunkBuf := make([]byte, spiChunkRows*DisplayW*2)

	rowsBuffered := 0
	for y := 0; y < DisplayH; y++ {
		// Encode one row.
		imgY := b.Min.Y + y
		for x := 0; x < DisplayW; x++ {
			imgX := b.Min.X + x
			var r, g, bv uint8
			if x < w && y < h {
				c := color.RGBAModel.Convert(img.At(imgX, imgY)).(color.RGBA)
				r, g, bv = c.R, c.G, c.B
			}
			rgb565 := (uint16(r)>>3)<<11 | (uint16(g)>>2)<<5 | uint16(bv)>>3
			base := x * 2
			rowBuf[base] = byte(rgb565 >> 8)
			rowBuf[base+1] = byte(rgb565)
		}
		// Append to chunk.
		copy(chunkBuf[rowsBuffered*DisplayW*2:], rowBuf)
		rowsBuffered++

		if rowsBuffered == spiChunkRows || y == DisplayH-1 {
			if err := d.writeData(chunkBuf[:rowsBuffered*DisplayW*2]); err != nil {
				return err
			}
			rowsBuffered = 0
		}
	}
	return nil
}

// Fill paints the entire display with a solid colour.
func (d *GC9A01) Fill(c color.RGBA) error {
	if err := d.SetWindow(0, 0, DisplayW-1, DisplayH-1); err != nil {
		return err
	}
	r5 := uint16(c.R) >> 3
	g6 := uint16(c.G) >> 2
	b5 := uint16(c.B) >> 3
	px := uint16(r5<<11) | uint16(g6<<5) | b5
	hi, lo := byte(px>>8), byte(px)

	buf := make([]byte, spiChunkRows*DisplayW*2)
	for i := 0; i < len(buf); i += 2 {
		buf[i], buf[i+1] = hi, lo
	}
	total := DisplayW * DisplayH
	sent := 0
	for sent < total {
		batch := spiChunkRows * DisplayW
		if sent+batch > total {
			batch = total - sent
		}
		if err := d.writeData(buf[:batch*2]); err != nil {
			return err
		}
		sent += batch
	}
	return nil
}
