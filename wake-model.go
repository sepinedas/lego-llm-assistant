package main

import (
	"encoding/json"
	"fmt"
	"log"

	vosk "github.com/hekt/vosk-api/go"
)

type VoskResponse struct {
	Text string `json:"text"`
}

func VoskRecognizer(sampleRate float64) *vosk.VoskRecognizer {
	model, err := vosk.NewModel("model")
	if err != nil {
		log.Fatal(err)
	}

	grammarJSON := `["maya"]`
	rec, err := vosk.NewRecognizerGrm(model, sampleRate, []byte(grammarJSON))
	if err != nil {
		log.Fatal(err)
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

		fmt.Println(string(res))

		if response.Text == "maya" {
			cb()
		}
	}
}
