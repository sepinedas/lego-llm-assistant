package main

import (
	"context"
	"log"

	"google.golang.org/genai"
)

func Session(ctx context.Context) (*genai.Session, error) {
	model := "gemini-3.1-flash-live-preview"
	client, err := genai.NewClient(ctx, nil)
	if err != nil {
		// Log fatal error if client creation fails (e.g., invalid config, authentication issues).
		log.Fatal("create client error: ", err)
		return nil, err
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
				{Text: "Eres una asistente con un tono de voz simpatico y acento tico."},
			},
		},
	})
	if err != nil {
		// Log fatal error if connecting to the model fails (e.g., network issues, invalid model name).
		log.Fatal("connect to model error: ", err)
	}

	return session, nil
}
