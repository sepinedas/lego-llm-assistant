package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"google.golang.org/genai"
)

func extractConfirmed(args map[string]any) bool {
	if args == nil {
		return false
	}
	switch v := args["confirmed"].(type) {
	case bool:
		return v
	case json.Number:
		return v.String() == "1"
	}
	return false
}

var disableSpeechTool = &genai.Tool{
	FunctionDeclarations: []*genai.FunctionDeclaration{
		{
			Name:        "endSession",
			Description: "Llama a esta función cuando el usuario se despida, te pida dormir o te diga que cierres los ojos. Termina la sesión de voz actual.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"confirmed": {
						Type:        genai.TypeBoolean,
						Description: "Siempre pasa true para confirmar el cierre de la sesión.",
					},
				},
				Required: []string{"confirmed"},
			},
		},
	},
}

func Session(ctx context.Context, onResponse func(data []byte), onToolCall func(), audio <-chan []byte, end chan<- bool) (*genai.Session, error) {
	model := "gemini-3.1-flash-live-preview"
	client, err := genai.NewClient(ctx, nil)
	if err != nil {
		// Log fatal error if client creation fails (e.g., invalid config, authentication issues).
		return nil, err
	}

	session, err := client.Live.Connect(ctx, model, &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
		SpeechConfig: &genai.SpeechConfig{
			VoiceConfig: &genai.VoiceConfig{
				PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{
					VoiceName: "zephyr", // Options typically include: Aoede, Charon, Fenrir, Kore, Puck
				},
			},
		},
		// Optional: System instructions to guide the persona
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{
				{Text: `Eres una asistente con un tono de voz simpatico y acento tico.
					Tu nombre es Maya. Vives en Condominio Alexa, casa #58, San Pablo, Heredia, Costa Rica.
					Estas hecha de piezas de lego, y te gusta contar cuentos y chistes.
					Cuando el usuario diga algo que signifique adiós, vete a dormir, cierra los ojos,
					hasta luego, buenas noches, detente, para o cualquier frase similar de despedida o sueño,
					DEBES llamar inmediatamente a la herramienta endSession con confirmed=true.
					No digas palabras de despedida antes de llamar a la herramienta; simplemente llámala.`},
			},
		},
		Tools: []*genai.Tool{disableSpeechTool},
	})
	if err != nil {
		// Log fatal error if connecting to the model fails (e.g., network issues, invalid model name).
		return nil, err
	}

	go func() {
		for {
			select {
			case data := <-audio:
				err := session.SendRealtimeInput(genai.LiveRealtimeInput{
					Audio: &genai.Blob{
						MIMEType: "audio/pcm;rate=16000",
						Data:     data,
					},
				})
				if err != nil {
					log.Panic("Error sending audio: %v", err)
					return
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
			default:
				response, err := session.Receive()
				if err != nil {
					// Log fatal error if receiving from the GenAI service fails (e.g., connection closed, network error).
					log.Panic("Error receiving server response: %v", err)
					return
				}
				if response.GoAway != nil {
					fmt.Println("Closing session")
					end <- true
					return
				}

				if response.ToolCall != nil {
					for _, fc := range response.ToolCall.FunctionCalls {
						if fc.Name == "endSession" {
							confirmed := extractConfirmed(fc.Args)
							fmt.Printf("\n[llamada a herramienta] endSession(confirmed=%v)\n", confirmed)

							_ = session.SendToolResponse(genai.LiveToolResponseInput{
								FunctionResponses: []*genai.FunctionResponse{
									{
										ID:   fc.ID,
										Name: fc.Name,
										Response: map[string]any{
											"status": "ok",
										},
									},
								},
							})
							onToolCall()
						}
					}
				}
				if response.ServerContent != nil && response.ServerContent.ModelTurn != nil {
					for _, part := range response.ServerContent.ModelTurn.Parts {
						if part.InlineData != nil {
							onResponse(part.InlineData.Data)
						}
					}
				}
			}
		}
	}()

	return session, nil
}
