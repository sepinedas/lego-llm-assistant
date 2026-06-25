package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/smallnest/ringbuffer"
	"google.golang.org/genai"
)

const commandTimeout = 3 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rb := ringbuffer.New(1024 * 4096)

	recMaya := VoskRecognizer(InputSampleRate, `["maya"]`)

	isMicOpen := false
	isCommandOpen := false
	fatialState := flag.String("state", "asleep", "")
	baseFatialState := *fatialState

	go initDisplay(fatialState)

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
		if (rb.IsEmpty() && isMicOpen) || isCommandOpen {
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

	defer showCommandEnabled(false)

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
				const maxAttempts = 4
				var session *genai.Session
				var err error
				backoff := 500 * time.Millisecond

				for attempt := 1; attempt <= maxAttempts; attempt++ {
					session, err = Session(ctx, func(data []byte) { rb.Write(data) }, func() {
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
						inAudio, endSession)
					if err == nil {
						break
					}

					// log and decide whether to retry
					fmt.Printf("failed to open session (attempt %d/%d): %v\n", attempt, maxAttempts, err)
					// simple heuristic: if the error message mentions "expired" or "token", retry after backoff
					lerr := strings.ToLower(err.Error())
					if strings.Contains(lerr, "expired") || strings.Contains(lerr, "token") || errors.Is(err, context.DeadlineExceeded) {
						time.Sleep(backoff)
						backoff *= 2
						continue
					}

					// otherwise give up immediately
					break
				}

				if err != nil {
					fmt.Printf("Could not start session: %v\n", err)
					enableSpeech(false)
					continue
				}

				enableSpeech(true)
				fmt.Println("Opening new session.")
				<-endSession
				fmt.Println("Closing session.")
				enableSpeech(false)
				err = session.Close()
				if err != nil {
					fmt.Printf("error closing session: %v\n", err)
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
