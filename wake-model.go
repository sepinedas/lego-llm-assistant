package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	vosk "github.com/hekt/vosk-api/go"
)

type VoskResponse struct {
	Text string `json:"text"`
}

func VoskRecognizer(sampleRate float64) *vosk.VoskRecognizer {
	modelPath := os.Getenv("VOSK_MODEL")
	model, err := vosk.NewModel(modelPath)
	if err != nil {
		log.Panic(err)
	}

	grammarJSON := `["maya"]`
	rec, err := vosk.NewRecognizerGrm(model, sampleRate, []byte(grammarJSON))
	if err != nil {
		log.Panic(err)
	}
	rec.SetWords(1)

	return rec
}

func Recognize(rec *vosk.VoskRecognizer, data []byte, cb func()) {
	if rec.AcceptWaveform(data) != 0 {
		res := rec.FinalResult()

		// 2. Parse the JSON
		var response VoskResponse
		if err := json.Unmarshal(res, &response); err != nil {
			fmt.Println("Error parsing JSON:", err)
			return
		}

		if response.Text == "maya" {
			cb()
		}
	}
}
