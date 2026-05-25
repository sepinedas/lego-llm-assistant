package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/smallnest/ringbuffer"
	"periph.io/x/conn/v3/driver/driverreg"
	"periph.io/x/host/v3"
)

const commandTimeout = 3 * time.Second

func initDisplay() {
	if _, err := host.Init(); err != nil {
		log.Fatal(err)
	}
	if _, err := driverreg.Init(); err != nil {
		log.Fatal(err)
	}

	mouth, err := openDisplay("SPI0.0", "GPIO25", "GPIO27")
	if err != nil {
		log.Fatal("mouth:", err)
	}
	defer mouth.Close()

	eye, err := openDisplay("SPI0.1", "GPIO22", "GPIO23")
	if err != nil {
		log.Fatal("eye:", err)
	}
	defer eye.Close()

	log.Println("Photo-realistic displays running at 30 fps")

	lidSpr := newSpring(1.0, 265, 20)
	smileSpr := newSpring(0.72, 3.5, 3.2)

	const fps = 30
	frameDur := time.Second / fps
	dt := 1.0 / float64(fps)
	phIdx, phStart, animStart := 0, time.Now(), time.Now()

	for {
		t0 := time.Now()
		animT := t0.Sub(animStart).Seconds()
		if time.Since(phStart) >= sequence[phIdx].dur {
			phIdx = (phIdx + 1) % len(sequence)
			phStart = time.Now()
		}
		ph := sequence[phIdx]

		var il, is float64
		if ph.lidTarget > 0.85 {
			il = 0.04 * math.Sin(animT*0.91)
			is = 0.04 * math.Sin(animT*0.77)
		}
		lidSpr.tick(ph.lidTarget+il, dt)
		smileSpr.tick(ph.smileTarget+is, dt)

		lid := clampF(lidSpr.pos, 0, 1)
		smile := clampF(smileSpr.pos, 0, 1)

		eyeImg := renderEye(lid, animT, ph.zzz)
		mouthImg := renderMouth(smile, ph.asleep, animT)

		if err := eye.dev.draw(eyeImg); err != nil {
			log.Println("eye:", err)
		}
		if err := mouth.dev.draw(mouthImg); err != nil {
			log.Println("mouth:", err)
		}

		if s := frameDur - time.Since(t0); s > 0 {
			time.Sleep(s)
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
