package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/smallnest/ringbuffer"
	"periph.io/x/host/v3"
)

const commandTimeout = 3 * time.Second

func initDisplay() {
	genOnly := flag.Bool("generate", false, "generate PNG frames and exit")
	flag.Parse()

	if *genOnly {
		fmt.Println("Generating animation frames …")
		if err := generateFrames(framesDir); err != nil {
			log.Fatalf("frame generation failed: %v", err)
		}
		fmt.Println("Done.")
		return
	}

	// ── periph.io host init ──────────────────────────────────────────────────
	if _, err := host.Init(); err != nil {
		log.Fatalf("periph host init: %v", err)
	}

	// ── GPIO pins ────────────────────────────────────────────────────────────
	dcLeft := mustGPIO(pinDCLeft)
	dcRight := mustGPIO(pinDCRight)
	rst := mustGPIO(pinRST)
	blLeft := mustGPIO(pinBLLeft)
	blRight := mustGPIO(pinBLRight)

	// ── SPI connections ──────────────────────────────────────────────────────
	connLeft := mustSPI(spiLeft)
	connRight := mustSPI(spiRight)

	// ── Display drivers ──────────────────────────────────────────────────────
	dispLeft := NewGC9A01(connLeft, dcLeft, rst, blLeft)
	dispRight := NewGC9A01(connRight, dcRight, rst, blRight)

	log.Println("Initialising displays …")
	if err := dispLeft.Init(); err != nil {
		log.Fatalf("left display init: %v", err)
	}
	// Right display shares the RST line; the pin is already high after the
	// left display's init, so just run the register sequence again.
	if err := dispRight.Init(); err != nil {
		log.Fatalf("right display init: %v", err)
	}
	log.Println("Displays ready.")

	// ── Frame loading ────────────────────────────────────────────────────────
	log.Println("Loading left-eye frames …")
	framesLeft, err := loadFrames(framesDir + "/left")
	if err != nil {
		log.Fatalf("left frames: %v\n(run with -generate first)", err)
	}
	log.Println("Loading right-eye frames …")
	framesRight, err := loadFrames(framesDir + "/right")
	if err != nil {
		log.Fatalf("right frames: %v", err)
	}

	// ── Shared state ─────────────────────────────────────────────────────────
	var sharedSt atomic.Int32
	sharedSt.Store(int32(StateNeutral))

	// ── Animators ────────────────────────────────────────────────────────────
	animLeft := NewAnimator("left", dispLeft, framesLeft, &sharedSt)
	animRight := NewAnimator("right", dispRight, framesRight, &sharedSt)

	go animLeft.Run()
	go animRight.Run()
	log.Println("Animation running. Commands: n=neutral  s=speaking  a=asleep  q=quit")

	// ── Demo auto-cycle (remove if you want manual control only) ─────────────
	go demoCycle(&sharedSt)

	// ── Stdin command reader ──────────────────────────────────────────────────
	stdinCh := make(chan byte, 4)
	go readStdin(stdinCh)

	// ── Signal / quit handling ────────────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case cmd := <-stdinCh:
			if cmd == 'q' || cmd == 'Q' {
				log.Println("Quit command received.")
				animLeft.Stop()
				animRight.Stop()
				_ = dispLeft.Fill(colBlack)
				_ = dispRight.Fill(colBlack)
				return
			}
			handleCmd(cmd, &sharedSt)
		case <-sigCh:
			log.Println("Shutting down …")
			animLeft.Stop()
			animRight.Stop()
			// Blank both displays on exit.
			_ = dispLeft.Fill(colBlack)
			_ = dispRight.Fill(colBlack)
			return
		}
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rb := ringbuffer.New(1024 * 4096)

	recMaya := VoskRecognizer(InputSampleRate, `["maya"]`)
	recBye := VoskRecognizer(InputSampleRate, `["adios", "alto"]`)

	isMicOpen := false
	isCommandOpen := false

	timer := time.NewTimer(0)

	Playback(rb, &isCommandOpen)

	endSession := make(chan bool)
	inAudio := make(chan []byte, 1)
	startSession := make(chan bool)

	enableSpeech := func(active bool) {
		if isMicOpen && active && rb.IsEmpty() {
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
		Recognize(recMaya, data, func(text string) {
			if text == "maya" {
				enableSpeech(true)

				select {
				case startSession <- true:
				default:
				}
			}
		})
		Recognize(recBye, data, func(text string) {
			if (text == "alto" || text == "adios") && isCommandOpen && rb.IsEmpty() {
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

	go initDisplay()

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
