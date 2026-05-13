package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/smallnest/ringbuffer"
	"google.golang.org/genai"
	"periph.io/x/host/v3"
)

func main() {
	ctx := context.Background()

	cMic := make(chan []byte)
	rb := ringbuffer.New(1024 * 2048)

	if _, err := host.Init(); err != nil {
		log.Fatal(err)
	}

	Capture(cMic)
	Playback(rb)
	timer := time.NewTimer(0)

	rec := VoskRecognizer()
	isSpeechEnabled := false

	session, err := Session(ctx)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer session.Close()

	showActive(true)
	defer showActive(false)

	go func() {
		for {
			// Receive the next message from the GenAI service session.
			response, err := session.Receive()
			if err != nil {
				// Log fatal error if receiving from the GenAI service fails (e.g., connection closed, network error).
				log.Fatal("receive model response error: ", err)
			}

			if response.ServerContent != nil && response.ServerContent.ModelTurn != nil {
				for _, part := range response.ServerContent.ModelTurn.Parts {
					if part.InlineData != nil {
						rb.Write(part.InlineData.Data)
					}
				}
			}
		}
	}()

	go func() {
		for {
			if !rb.IsEmpty() {
				fmt.Println("Stop timer.")
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

				timer.Reset(10 * time.Second)
			}
		}
	}()

	go func() {
		for {
			<-timer.C
			fmt.Println("Speech disabled.")
			isSpeechEnabled = false
			showSpeechEnabled(false)
		}
	}()

	for {
		data := <-cMic
		if rb.IsEmpty() && isSpeechEnabled {
			err := session.SendRealtimeInput(genai.LiveRealtimeInput{
				Audio: &genai.Blob{
					MIMEType: "audio/pcm;rate=16000",
					Data:     data,
				},
			})
			if err != nil {
				log.Printf("Error sending audio: %v", err)
			}
		} else {
			Recognize(rec, data, func() {
				fmt.Println("Speech enabled.")
				isSpeechEnabled = true
				showSpeechEnabled(true)
				timer.Reset(10 * time.Second)
			})
		}
	}
}
