package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time" // 1. Imported time package

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

// 2. Defined the current time tool
var getCurrentTimeTool = &genai.Tool{
	FunctionDeclarations: []*genai.FunctionDeclaration{
		{
			Name:        "getCurrentTime",
			Description: "Obtiene la fecha y hora actual en Costa Rica.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
			},
		},
	},
}

func Session(ctx context.Context, onResponse func(data []byte), onToolCall func(), audio <-chan []byte, end chan<- bool) (*genai.Session, error) {
	model := "gemini-3.1-flash-live-preview"
	client, err := genai.NewClient(ctx, nil)
	if err != nil {
		return nil, err
	}

	session, err := client.Live.Connect(ctx, model, &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
		SpeechConfig: &genai.SpeechConfig{
			VoiceConfig: &genai.VoiceConfig{
				PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{
					VoiceName: "zephyr",
				},
			},
		},
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{
				{Text: `Eres una asistente con un tono de voz simpatico y acento tico.
					Tu nombre es Maya. Vives en Heredia, Costa Rica.
					Estas hecha de piezas de lego, y te gusta contar cuentos y hablar de temas educativos.
					Cuando el usuario diga algo que signifique adiós, vete a dormir, cierra los ojos,
					hasta luego, buenas noches, detente, para o cualquier frase similar de despedida o sueño,
					DEBES llamar inmediatamente a la herramienta endSession con confirmed=true.
					No digas palabras de despedida antes de llamar a la herramienta; simplemente llámala.
					Si te preguntan la hora o fecha actual, utiliza la herramienta getCurrentTime.`},
			},
		},
		// 3. Added getCurrentTimeTool to the session configuration
		Tools: []*genai.Tool{disableSpeechTool, getCurrentTimeTool},
	})
	if err != nil {
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

						// 4. Handle the "getCurrentTime" tool call
						if fc.Name == "getCurrentTime" {
							// Load Costa Rica timezone to match Maya's persona
							loc, err := time.LoadLocation("America/Costa_Rica")
							var now time.Time
							if err != nil {
								now = time.Now() // Fallback to local time if timezone missing
							} else {
								now = time.Now().In(loc)
							}

							timeStr := now.Format("2006-01-02 15:04:05 Mon")
							fmt.Printf("\n[llamada a herramienta] getCurrentTime() -> %s\n", timeStr)

							_ = session.SendToolResponse(genai.LiveToolResponseInput{
								FunctionResponses: []*genai.FunctionResponse{
									{
										ID:   fc.ID,
										Name: fc.Name,
										Response: map[string]any{
											"currentTime": timeStr,
										},
									},
								},
							})
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
