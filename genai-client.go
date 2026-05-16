package main

import (
	"context"
	"fmt"
	"log"

	"google.golang.org/genai"
)

func Session(ctx context.Context, cb func(data []byte), audio <-chan []byte, end chan bool) error {
	model := "gemini-3.1-flash-live-preview"
	client, err := genai.NewClient(ctx, nil)
	if err != nil {
		// Log fatal error if client creation fails (e.g., invalid config, authentication issues).
		return err
	}

	session, err := client.Live.Connect(ctx, model, &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
		SpeechConfig: &genai.SpeechConfig{
			VoiceConfig: &genai.VoiceConfig{
				PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{
					VoiceName: "Aoede", // Options typically include: Aoede, Charon, Fenrir, Kore, Puck
				},
			},
		},
		// Optional: System instructions to guide the persona
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{
				{Text: `Eres una asistente con un tono de voz simpatico y acento tico.
					Tu nombre es Maya. Vives en Condominio Alexa, casa #58, San Pablo, Heredia, Costa Rica.
					Estas hecha de piezas de lego, y tu creador es Eduardo Pineda.`},
			},
		},
	})
	if err != nil {
		// Log fatal error if connecting to the model fails (e.g., network issues, invalid model name).
		return err
	}

	go func() {
		for {
			select {
			case <-end:
				return
			case data := <-audio:
				err := session.SendRealtimeInput(genai.LiveRealtimeInput{
					Audio: &genai.Blob{
						MIMEType: "audio/pcm;rate=16000",
						Data:     data,
					},
				})
				if err != nil {
					log.Printf("Error sending audio: %v", err)
				}
			case <-ctx.Done():
				session.Close()
				return
			}
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				session.Close()
				return
			case <-end:
				session.Close()
				return
			default:
				response, err := session.Receive()
				if err != nil {
					// Log fatal error if receiving from the GenAI service fails (e.g., connection closed, network error).
					fmt.Println("Error receiving server response: %v", err)
					return
				}
				if response.GoAway != nil {
					fmt.Println("Closing session")
					session.Close()
					end <- true
					return
				}

				if response.ServerContent != nil && response.ServerContent.ModelTurn != nil {
					for _, part := range response.ServerContent.ModelTurn.Parts {
						if part.InlineData != nil {
							cb(part.InlineData.Data)
						}
					}
				}
			}
		}
	}()

	return nil
}
