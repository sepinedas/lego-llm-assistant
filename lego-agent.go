package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/gen2brain/malgo"
	"github.com/smallnest/ringbuffer"
	"google.golang.org/genai"
)

func main() {
	ctx := context.Background()
	cMic := make(chan []byte)
	rb := ringbuffer.New(1024 * 512)
	malgoCtx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		fmt.Printf("LOG <%v>\n", message)
	})
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer func() {
		_ = malgoCtx.Uninit()
		malgoCtx.Free()
	}()

	recorder := NewRecorder(cMic, malgoCtx.Context)
	player := NewPlayer(rb, malgoCtx.Context)

	client, err := genai.NewClient(ctx, nil)
	if err != nil {
		// Log fatal error if client creation fails (e.g., invalid config, authentication issues).
		log.Fatal("create client error: ", err)
		return
	}
	model := "gemini-3.1-flash-live-preview"

	session, err := client.Live.Connect(ctx, model, &genai.LiveConnectConfig{ResponseModalities: []genai.Modality{genai.ModalityAudio}})
	if err != nil {
		// Log fatal error if connecting to the model fails (e.g., network issues, invalid model name).
		log.Fatal("connect to model error: ", err)
	}
	defer session.Close() // Ensure session is closed when the handler exits

	go player.start()
	go recorder.record()

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
