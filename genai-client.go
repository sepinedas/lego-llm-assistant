package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	tavily "github.com/iamwavecut/go-tavily"
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

// webSearchTool lets Maya look up real-world information she doesn't know.
var webSearchTool = &genai.Tool{
	FunctionDeclarations: []*genai.FunctionDeclaration{
		{
			Name:        "webSearch",
			Description: "Busca información actualizada en internet cuando no sabes la respuesta a una pregunta. Úsala para noticias, datos recientes, preguntas de cultura general, o cualquier tema que n[...]",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"query": {
						Type:        genai.TypeString,
						Description: "La consulta de búsqueda en el idioma más apropiado para encontrar la respuesta.",
					},
				},
				Required: []string{"query"},
			},
		},
	},
}

// newTavilyClient builds a Tavily client from the TAVILY_API_KEY env var.
func newTavilyClient() *tavily.Client {
	apiKey := os.Getenv("TAVILY_API_KEY")
	if apiKey == "" {
		log.Println("[tavily] WARNING: TAVILY_API_KEY is not set; webSearch will fail")
	}
	httpClient := &http.Client{Timeout: 15 * time.Second}
	return tavily.New(apiKey, &tavily.Options{HTTPClient: httpClient})
}

// handleWebSearch calls Tavily and returns a concise answer string to send back to Gemini.
func handleWebSearch(ctx context.Context, tc *tavily.Client, query string) string {
	fmt.Printf("\n[llamada a herramienta] webSearch(query=%q)\n", query)

	result, err := tc.Search(ctx, query, &tavily.SearchOptions{
		SearchDepth:   string(tavily.SearchDepthAdvanced),
		MaxResults:    5,
		IncludeAnswer: true,
	})
	if err != nil {
		fmt.Printf("[tavily] error: %v\n", err)
		return fmt.Sprintf("No pude obtener resultados de búsqueda: %v", err)
	}

	// Prefer the AI-generated answer when available.
	if result.Answer != "" {
		return result.Answer
	}

	// Fall back to a summary of the top snippets.
	var sb strings.Builder
	for i, r := range result.Results {
		if i >= 3 {
			break
		}
		fmt.Fprintf(&sb, "- %s: %s\n", r.Title, r.Content)
	}
	if sb.Len() == 0 {
		return "No encontré información relevante para esa consulta."
	}
	return sb.String()
}

func Session(ctx context.Context, onResponse func(data []byte), onSleepCall func(), onWebSearchCall func(finish bool), audio <-chan []byte, end chan<- bool) (*genai.Session, error) {
	model := "gemini-3.1-flash-live-preview"
	client, err := genai.NewClient(ctx, nil)
	if err != nil {
		return nil, err
	}

	tavilyClient := newTavilyClient()

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
					Si te preguntan la hora o fecha actual, utiliza la herramienta getCurrentTime.
					Si no sabes la respuesta a una pregunta o necesitas información actualizada,
					dile al usuario que vas a buscar en internet, luego utiliza la herramienta webSearch para buscarla en internet antes de responder.`},
			},
		},
		Tools: []*genai.Tool{disableSpeechTool, getCurrentTimeTool, webSearchTool},
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
					// don't panic; tell the caller the session ended
					fmt.Printf("Error sending audio: %v\n", err)
					end <- true
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
					// don't panic; log and signal end so main can decide to restart the session
					fmt.Printf("Error receiving server response: %v\n", err)
					end <- true
					return
				}
				if response.GoAway != nil {
					fmt.Println("Closing session")
					end <- true
					return
				}

				if response.ToolCall != nil {
					for _, fc := range response.ToolCall.FunctionCalls {
						switch fc.Name {

						case "endSession":
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
							onSleepCall()

						case "getCurrentTime":
							loc, err := time.LoadLocation("America/Costa_Rica")
							var now time.Time
							if err != nil {
								now = time.Now()
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

						case "webSearch":
							onWebSearchCall(false)
							query, _ := fc.Args["query"].(string)
							answer := handleWebSearch(ctx, tavilyClient, query)
							_ = session.SendToolResponse(genai.LiveToolResponseInput{
								FunctionResponses: []*genai.FunctionResponse{
									{
										ID:   fc.ID,
										Name: fc.Name,
										Response: map[string]any{
											"answer": answer,
										},
									},
								},
							})
							onWebSearchCall(true)
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
