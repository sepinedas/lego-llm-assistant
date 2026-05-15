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
	"periph.io/x/host/v3"
)

const speechTimeout = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rb := ringbuffer.New(1024 * 4096)

	if _, err := host.Init(); err != nil {
		log.Panic(err)
	}

	Playback(rb)
	timer := time.NewTimer(0)

	rec := VoskRecognizer(InputSampleRate)
	isSpeechEnabled := false

	endSession := make(chan bool)
	inAudio := make(chan []byte, 1)
	startSession := make(chan bool)

	handleInputAudio := func(data []byte, framecount uint32) {
		if rb.IsEmpty() && isSpeechEnabled {
			inAudio <- data
		} else {
			Recognize(rec, IncreaseVolumeBytes(data, 1.5), func() {
				fmt.Println("Speech enabled.")
				isSpeechEnabled = true
				showSpeechEnabled(true)
				timer.Reset(speechTimeout)

				select {
				case startSession <- true:
				default:
				}
			})
		}
	}
	Capture(handleInputAudio, InputSampleRate)

	showActive(true)
	defer showActive(false)
	defer showSpeechEnabled(false)

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
			default:
				if !rb.IsEmpty() {
					// 1. Stop the timer and check if it was already expired/stopped
					if !timer.Stop() {
						// 2. Drain the channel if it was not drained yet
						select {
						case <-timer.C:
						default:
						}
					}

					for !rb.IsEmpty() {
					}

					timer.Reset(speechTimeout)
				}
			}
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return

			case <-timer.C:
				fmt.Println("Speech disabled.")
				isSpeechEnabled = false
				showSpeechEnabled(false)
			}
		}
	}()

	<-ctx.Done()
}
