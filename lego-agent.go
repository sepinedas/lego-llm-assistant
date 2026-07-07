package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/smallnest/ringbuffer"
	"google.golang.org/genai"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rb := ringbuffer.New(1024 * 4096)

	recMaya := VoskRecognizer(InputSampleRate, `["maya"]`)

	isMicOpen := false
	fatialState := flag.String("state", "asleep", "")
	baseFatialState := *fatialState

	go initDisplay(fatialState)

	Playback(rb)

	inAudio := make(chan []byte, 1)
	startSession := make(chan bool)

	enableSpeech := func(active bool) {
		isMicOpen = active
		if active {
			fmt.Println("Speech enabled.")
			baseFatialState = "neutral"
		} else {
			fmt.Println("Speech disabled.")
			baseFatialState = "asleep"
			*fatialState = "asleep"
		}
	}

	handleInputAudio := func(data []byte, framecount uint32) {
		if isMicOpen {
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
	}
	Capture(handleInputAudio, InputSampleRate)

	go func() {
		for {
			for !rb.IsEmpty() {
				*fatialState = "speaking"
			}
			if isMicOpen {
				*fatialState = baseFatialState
			}
		}
	}()

	go func() {
		for {
			select {
			case <-startSession:
				endSession := make(chan bool)
				const maxAttempts = 4
				var session *genai.Session
				var err error

				for attempt := 1; attempt <= maxAttempts; attempt++ {
					session, err = Session(ctx)
					if err == nil {
						break
					}

					// log and decide whether to retry
					fmt.Printf("failed to open session (attempt %d/%d): %v\n", attempt, maxAttempts, err)
				}

				if err != nil {
					fmt.Printf("Could not start session: %v\n", err)
					enableSpeech(false)
					continue
				}
				defer session.Close()

				go handleSendAudio(ctx, *session, inAudio, endSession)
				go handleReceiveMessages(ctx, *session, func(data []byte) { rb.Write(data) }, func() {
					rb.Reset()
					enableSpeech(false)
				},
					func(finish bool) {
						if finish {
							baseFatialState = "neutral"
						} else {
							baseFatialState = "thinking"
						}
					},
					endSession)

				enableSpeech(true)
				fmt.Println("Opening new session.")
				<-endSession
				fmt.Println("Closing session.")
				enableSpeech(false)
				close(endSession)
				err = session.Close()
				if err != nil {
					fmt.Printf("error closing session: %v\n", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	<-ctx.Done()
}
