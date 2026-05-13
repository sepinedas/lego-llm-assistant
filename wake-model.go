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

func VoskRecognizer() *vosk.VoskRecognizer {
	model, err := vosk.NewModel("model")
	if err != nil {
		log.Fatal(err)
	}

	sampleRate := 16000.0
	rec, err := vosk.NewRecognizer(model, sampleRate)
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

		fmt.Println(response.Text)

		if response.Text == "raya" {
			cb()
		}
	}
}
