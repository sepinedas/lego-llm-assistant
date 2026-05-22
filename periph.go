package main

import (
	"math"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/host/v3/rpi"
)

func showCommandEnabled(active bool) {
	if active {
		rpi.P1_33.Out(gpio.High)
	} else {
		rpi.P1_33.Out(gpio.Low)
	}
}

func showSpeechEnabled(active bool) {
	if active {
		rpi.P1_32.Out(gpio.High)
	} else {
		rpi.P1_32.Out(gpio.Low)
	}
}

type GC9A01 struct {
	spiConn spi.Conn
	dcPin   gpio.PinIO
	rstPin  gpio.PinIO
}

// WriteCommand drops DC Low and sends a command byte
func (d *GC9A01) WriteCommand(cmd byte) error {
	if err := d.dcPin.Out(gpio.Low); err != nil {
		return err
	}
	return d.spiConn.Tx([]byte{cmd}, nil)
}

// WriteData raises DC High and sends data bytes
func (d *GC9A01) WriteData(data []byte) error {
	if err := d.dcPin.Out(gpio.High); err != nil {
		return err
	}
	return d.spiConn.Tx(data, nil)
}

// Init Sequence translated from standard driver registers
func (d *GC9A01) InitLCD() error {
	// Hardware Reset
	d.rstPin.Out(gpio.Low)
	time.Sleep(20 * time.Millisecond)
	d.rstPin.Out(gpio.High)
	time.Sleep(150 * time.Millisecond)

	// Initialization Command/Data list
	initCmds := []struct {
		cmd  byte
		data []byte
	}{
		{0xEF, []byte{}},
		{0xEB, []byte{0x14}},
		{0xFE, []byte{}},
		{0xEF, []byte{}},
		{0xEB, []byte{0x14}},
		{0x84, []byte{0x40}},
		{0x85, []byte{0xFF}},
		{0x86, []byte{0xFF}},
		{0x87, []byte{0xFF}},
		{0x8E, []byte{0xFF}},
		{0x8F, []byte{0xFF}},
		{0x88, []byte{0x0A}},
		{0x89, []byte{0x21}},
		{0x8A, []byte{0x00}},
		{0x8B, []byte{0x80}},
		{0x8C, []byte{0x01}},
		{0x8D, []byte{0x01}},
		{0xB6, []byte{0x00, 0x20}}, // Display Function Control
		{0x36, []byte{0x08}},       // Memory Access Control (Orientation)
		{0x3A, []byte{0x05}},       // Pixel Format Set (16-bit color RGB565)
		{0x90, []byte{0x08, 0x08, 0x08, 0x08}},
		{0xBD, []byte{0x06}},
		{0xBC, []byte{0x00}},
		{0xFF, []byte{0x60, 0x01, 0x04}},
		{0xC3, []byte{0x13}}, // Power Control 2
		{0xC4, []byte{0x13}}, // Power Control 3
		{0xC9, []byte{0x22}}, // Power Control 4
		{0xBE, []byte{0x11}},
		{0xE1, []byte{0x10, 0x0E}},
		{0xDF, []byte{0x21, 0x0c, 0x02}},
		{0xF0, []byte{0x45, 0x09, 0x08, 0x08, 0x26, 0x2A}}, // Positive Gamma Correction
		{0xF1, []byte{0x43, 0x70, 0x72, 0x36, 0x37, 0x6F}}, // Negative Gamma Correction
		{0xED, []byte{0x1B, 0x0B}},
		{0xAE, []byte{0x77}},
		{0xCD, []byte{0x63}},
		{0x70, []byte{0x07, 0x07, 0x04, 0x0E, 0x0F, 0x09, 0x07, 0x08, 0x03}},
		{0xE8, []byte{0x34}},
		{0x62, []byte{0x18, 0x0D, 0x71, 0xED, 0x70, 0x70, 0x18, 0x0F, 0x71, 0xEF, 0x70, 0x70}},
		{0x63, []byte{0x18, 0x11, 0x71, 0xF1, 0x70, 0x70, 0x18, 0x13, 0x71, 0xF3, 0x70, 0x70}},
		{0x64, []byte{0x18, 0x46, 0x71, 0x7C, 0x70, 0x70, 0x18, 0x46, 0x71, 0x7C, 0x70, 0x70}},
		{0x66, []byte{0x3C, 0x00, 0xCD, 0x67, 0x45, 0x45, 0x10, 0x00, 0x00, 0x00}},
		{0x67, []byte{0x00, 0x3C, 0x00, 0x00, 0x00, 0x01, 0x54, 0x10, 0x32, 0x75}},
		{0x74, []byte{0x10, 0x85, 0x80, 0x00, 0x00, 0x4E, 0x00}},
		{0x98, []byte{0x3e, 0x07}},
		{0x35, []byte{0x00}}, // Tearing Effect Line On
		{0x21, []byte{}},     // Display Inversion On
		{0x11, []byte{}},     // Sleep Out
	}

	for _, pair := range initCmds {
		if err := d.WriteCommand(pair.cmd); err != nil {
			return err
		}
		if len(pair.data) > 0 {
			if err := d.WriteData(pair.data); err != nil {
				return err
			}
		}
	}

	time.Sleep(120 * time.Millisecond)
	return d.WriteCommand(0x29) // Display On
}

func (d *GC9A01) SetWindow(x0, y0, x1, y1 uint16) {
	d.WriteCommand(0x2A) // Column
	d.WriteData([]byte{byte(x0 >> 8), byte(x0), byte(x1 >> 8), byte(x1)})

	d.WriteCommand(0x2B) // Row
	d.WriteData([]byte{byte(y0 >> 8), byte(y0), byte(y1 >> 8), byte(y1)})

	d.WriteCommand(0x2C) // Memory Write
}

type PixelBuffer struct {
	Width  int
	Height int
	Buf    []byte
}

// NewPixelBuffer allocates memory for a full frame
func NewPixelBuffer(w, h int) *PixelBuffer {
	return &PixelBuffer{
		Width:  w,
		Height: h,
		Buf:    make([]byte, w*h*2),
	}
}

// SetPixel converts R, G, B (0-255) to RGB565 and updates the buffer index
func (pb *PixelBuffer) SetPixel(x, y int, r, g, b byte) {
	if x < 0 || x >= pb.Width || y < 0 || y >= pb.Height {
		return // Out of bounds safety check
	}

	// Pack RGB888 into RGB565 (16-bit integer)
	// Red: 5 bits, Green: 6 bits, Blue: 5 bits
	rgb565 := uint16(r&0xF8)<<8 | uint16(g&0xFC)<<3 | uint16(b&0xF8)>>3

	// Calculate buffer target byte index
	idx := (y*pb.Width + x) * 2

	// Screen expects Big-Endian bytes (High byte first)
	pb.Buf[idx] = byte(rgb565 >> 8)
	pb.Buf[idx+1] = byte(rgb565)
}

// Clear fills the entire canvas with a single base color
func (pb *PixelBuffer) Clear(r, g, b byte) {
	for y := 0; y < pb.Height; y++ {
		for x := 0; x < pb.Width; x++ {
			pb.SetPixel(x, y, r, g, b)
		}
	}
}

// DrawCircle creates a colored outline or solid disc
func (pb *PixelBuffer) DrawCircle(cx, cy, radius int, r, g, b byte, fill bool) {
	for y := cy - radius; y <= cy+radius; y++ {
		for x := cx - radius; x <= cx+radius; x++ {
			// Pathagorean theorem to find pixel distance from the center point
			dx := float64(x - cx)
			dy := float64(y - cy)
			dist := math.Sqrt(dx*dx + dy*dy)

			if fill {
				if dist <= float64(radius) {
					pb.SetPixel(x, y, r, g, b)
				}
			} else {
				// Outline thickness of roughly 1 pixel boundary
				if dist >= float64(radius-1) && dist <= float64(radius) {
					pb.SetPixel(x, y, r, g, b)
				}
			}
		}
	}
}

// DrawRect renders filled blocks or layout backgrounds
func (pb *PixelBuffer) DrawRect(x, y, w, h int, r, g, b byte) {
	for row := y; row < y+h; row++ {
		for col := x; col < x+w; col++ {
			pb.SetPixel(col, row, r, g, b)
		}
	}
}

func (d *GC9A01) PushBuffer(pb *PixelBuffer) error {
	// 1. Tell the display we want to draw the full 240x240 frame canvas
	d.SetWindow(0, 0, uint16(pb.Width-1), uint16(pb.Height-1))

	// 2. Define the maximum chunk size allowed by Linux sysfs (4096 bytes)
	const maxChunkSize = 4096
	data := pb.Buf
	totalBytes := len(data)

	// 3. Loop and transmit chunks sequentially
	for i := 0; i < totalBytes; i += maxChunkSize {
		end := i + maxChunkSize
		if end > totalBytes {
			end = totalBytes // Prevent slicing past the end of the array
		}

		// Send just this chunk
		if err := d.WriteData(data[i:end]); err != nil {
			return err
		}
	}

	return nil
}
