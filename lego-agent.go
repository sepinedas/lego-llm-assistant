package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/smallnest/ringbuffer"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

const commandTimeout = 5 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rb := ringbuffer.New(1024 * 4096)

	if _, err := host.Init(); err != nil {
		log.Panic(err)
	}

	rec := VoskRecognizer(InputSampleRate)
	isMicOpen := false
	isCommandOpen := false

	timer := time.NewTimer(0)

	Playback(rb, &isCommandOpen)

	endSession := make(chan bool)
	inAudio := make(chan []byte, 1)
	startSession := make(chan bool)

	enableSpeech := func(active bool) {
		if isMicOpen && active {
			isCommandOpen = true
			timer.Reset(commandTimeout)
			fmt.Println("Command enabled.")
			showCommandEnabled(true)
		}
		showSpeechEnabled(active)
		if active {
			fmt.Println("Speech enabled.")
		} else {
			fmt.Println("Speech disabled.")
		}
		isMicOpen = active
	}

	handleInputAudio := func(data []byte, framecount uint32) {
		if rb.IsEmpty() && isMicOpen {
			inAudio <- data
		}
		Recognize(rec, data, func(text string) {
			if text == "maya" {
				enableSpeech(true)

				select {
				case startSession <- true:
				default:
				}
			}
			if (text == "alto" || text == "adios") && isCommandOpen {
				go func() {
					endSession <- true
					enableSpeech(false)
					showCommandEnabled(false)
					rb.Reset()
				}()
			}
		})
	}
	Capture(handleInputAudio, InputSampleRate)

	defer showSpeechEnabled(false)
	defer showCommandEnabled(false)

	// Open the SPI bus (e.g., SPI0.0 on Raspberry Pi)
	bus, err := spireg.Open("SPI0.0")
	if err != nil {
		log.Fatalf("failed to open SPI: %v", err)
	}
	defer bus.Close()

	// Connect with max speed specs of GC9A01 (up to 40MHz, keeping 24MHz for safety)
	spiConn, err := bus.Connect(24*physic.MegaHertz, spi.Mode0, 8)
	if err != nil {
		log.Fatalf("failed to configure SPI connection: %v", err)
	}

	// Setup GPIO Control Pins (Modify pin strings based on your wiring setup)
	dc := gpioreg.ByName("GPIO22")
	rst := gpioreg.ByName("GPIO27")

	display := &GC9A01{
		spiConn: spiConn,
		dcPin:   dc,
		rstPin:  rst,
	}

	// Execute sequence to fire up the round screen
	if err := display.InitLCD(); err != nil {
		log.Fatalf("failed to initialize GC9A01 screen: %v", err)
	}

	log.Println("GC9A01 Successfully initialized!")

	go func() {
		for {
			select {
			case <-startSession:
				err := Session(ctx, func(data []byte) { rb.Write(data) }, inAudio, endSession)
				if err != nil {
					log.Panic(err)
				}
				fmt.Println("Opening new session.")
				<-endSession
				fmt.Println("Closing session.")
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				isCommandOpen = false
				showCommandEnabled(false)
			}
		}
	}()

	<-ctx.Done()
}
