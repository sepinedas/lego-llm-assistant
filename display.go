package main

// main.go — Entry point for the two-eye robot face on a pair of GC9A01
// 240×240 circular displays connected to a Raspberry Pi over SPI0.
//
// ─── Quick-start ────────────────────────────────────────────────────────────
//
//  1. Enable SPI0 with two chip-selects in /boot/config.txt:
//       dtparam=spi=on
//       dtoverlay=spi0-2cs   # provides SPI0.0 (CE0) and SPI0.1 (CE1)
//
//  2. Run once to generate the animation frames:
//       go run . -generate
//
//  3. Run the main animation loop:
//       go run .
//
// ─── Wiring (Raspberry Pi) ──────────────────────────────────────────────────
//
//  Signal     Left display (SPI0.0)    Right display (SPI0.1)
//  --------   ---------------------    ----------------------
//  SCLK       GPIO11 / pin 23          same
//  MOSI       GPIO10 / pin 19          same
//  CS         GPIO8  / pin 24          GPIO7 / pin 26
//  DC         GPIO24 / pin 18          GPIO23 / pin 16
//  RST        GPIO25 / pin 22          (shared)
//  BL         GPIO18 / pin 12          GPIO13 / pin 33
//  3.3 V      pin 1 or 17              pin 1 or 17
//  GND        pin 6, 9 …               same
//
// ─── State control ──────────────────────────────────────────────────────────
//
//  The program accepts single-character commands on stdin while running:
//    n  → neutral
//    s  → speaking
//    a  → asleep
//    q  → quit
//
//  Integrate with your robot's main controller by writing to the process's
//  stdin or by importing the logic as a library and calling SetState().

import (
	"bufio"
	"log"
	"os"
	"sync/atomic"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
)

// ────────────────────────────────────────────────────────────────────────────
// Configuration
// ────────────────────────────────────────────────────────────────────────────

const (
	framesDir  = "./frames"
	spiSpeedHz = 40_000_000 // 40 MHz — safe maximum for most GC9A01 modules

	// GPIO pin names as recognised by periph.io / gpioreg.
	pinDCLeft  = "GPIO22"
	pinDCRight = "GPIO23"
	pinRST     = "GPIO25" // shared reset
	pinBLLeft  = "GPIO18"
	pinBLRight = "GPIO13"

	// SPI bus names.
	spiLeft  = "SPI0.0"
	spiRight = "SPI0.1"
)

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

func mustGPIO(name string) gpio.PinOut {
	p := gpioreg.ByName(name)
	if p == nil {
		log.Fatalf("GPIO pin %q not found", name)
	}
	return p
}

func mustSPI(name string) spi.Conn {
	port, err := spireg.Open(name)
	if err != nil {
		log.Fatalf("open SPI %q: %v", name, err)
	}
	conn, err := port.Connect(physic.Frequency(spiSpeedHz)*physic.Hertz, spi.Mode0, 8)
	if err != nil {
		log.Fatalf("connect SPI %q: %v", name, err)
	}
	return conn
}

// demoCycle automatically advances through states so the display looks alive
// even without an external controller. Replace / remove for production use.
func demoCycle(st *atomic.Int32) {
	schedule := []struct {
		state    AnimState
		duration time.Duration
	}{
		{StateNeutral, 4 * time.Second},
		{StateSpeaking, 3 * time.Second},
		{StateNeutral, 2 * time.Second},
		{StateAsleep, 6 * time.Second},
		{StateNeutral, 3 * time.Second},
	}
	for {
		for _, s := range schedule {
			st.Store(int32(s.state))
			log.Printf("[demo] → %s", s.state)
			time.Sleep(s.duration)
		}
	}
}

// readStdin reads single-character commands from stdin.
func readStdin(ch chan<- byte) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}
		ch <- line[0]
	}
}

// handleCmd processes a single command byte (q/quit handled by caller).
func handleCmd(cmd byte, st *atomic.Int32) {
	switch cmd {
	case 'n', 'N':
		st.Store(int32(StateNeutral))
		log.Println("State → neutral")
	case 's', 'S':
		st.Store(int32(StateSpeaking))
		log.Println("State → speaking")
	case 'a', 'A':
		st.Store(int32(StateAsleep))
		log.Println("State → asleep")
	default:
		log.Printf("Unknown command %q  (n=neutral  s=speaking  a=asleep  q=quit)", cmd)
	}
}

// SetState is a public entry-point for embedding eyebot in a larger
// application — call this from whichever goroutine controls speech/sleep.
func SetState(st *atomic.Int32, s AnimState) {
	st.Store(int32(s))
}
