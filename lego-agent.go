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
)

const commandTimeout = 3 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rb := ringbuffer.New(1024 * 4096)

	recMaya := VoskRecognizer(InputSampleRate, `["maya"]`)
	recBye := VoskRecognizer(InputSampleRate, `["adios", "alto"]`)

	isMicOpen := false
	isCommandOpen := false
	fatialState := make(chan AnimState)

	go initDisplay(fatialState)

	timer := time.NewTimer(0)

	Playback(rb, &isCommandOpen)

	endSession := make(chan bool)
	inAudio := make(chan []byte, 1)
	startSession := make(chan bool)

	enableSpeech := func(active bool) {
		if isMicOpen && active && !rb.IsEmpty() {
			isCommandOpen = true
			timer.Reset(commandTimeout)
			fmt.Println("Command enabled.")
			showCommandEnabled(true)
		}
		isMicOpen = active
		if active {
			fmt.Println("Speech enabled.")
			fatialState <- Neutral
		} else {
			fmt.Println("Speech disabled.")
			fatialState <- Asleep
		}
	}

	handleInputAudio := func(data []byte, framecount uint32) {
		if rb.IsEmpty() && isMicOpen {
			inAudio <- data
		}
		Recognize(recMaya, data, func(text string) {
			if text == "maya" {
				select {
				case startSession <- true:
				default:
					enableSpeech(true)
				}
			}
		})
		Recognize(recBye, data, func(text string) {
			if (text == "alto" || text == "adios") && isCommandOpen && rb.IsEmpty() {
				go func() {
					enableSpeech(false)
					showCommandEnabled(false)
					rb.Reset()
				}()
			}
		})
	}
	Capture(handleInputAudio, InputSampleRate)

	defer showCommandEnabled(false)

	go func() {
		fatialState <- Asleep
	}()

	go func() {
		for {
			for !rb.IsEmpty() {
				fatialState <- Speaking
			}
			if isMicOpen {
				fatialState <- Neutral
			}
		}
	}()

	go func() {
		for {
			select {
			case <-startSession:
				session, err := Session(ctx, func(data []byte) { rb.Write(data) }, func() { enableSpeech(false) }, inAudio, endSession)
				enableSpeech(true)
				if err != nil {
					log.Panic(err)
				}
				fmt.Println("Opening new session.")
				<-endSession
				fmt.Println("Closing session.")
				enableSpeech(false)
				err = session.Close()
				if err != nil {
					log.Panic(err)
				}
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
