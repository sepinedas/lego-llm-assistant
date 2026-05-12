package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/smallnest/ringbuffer"
	"google.golang.org/genai"
	"periph.io/x/host/v3"
)

func main() {
	ctx := context.Background()
	cMic := make(chan []byte)
	rb := ringbuffer.New(1024 * 1024)

	if _, err := host.Init(); err != nil {
		log.Fatal(err)
	}

	Capture(cMic)
	Playback(rb)

	session, err := Session(ctx)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	showActive(true)
	defer session.Close()

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

	for {
		data := <-cMic
		if rb.IsEmpty() {
			err := session.SendRealtimeInput(genai.LiveRealtimeInput{
				Audio: &genai.Blob{
					MIMEType: "audio/pcm;rate=16000",
					Data:     data,
				},
			})
			if err != nil {
				log.Printf("Error sending audio: %v", err)
			}
		}
	}
}
