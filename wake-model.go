package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	vosk "github.com/hekt/vosk-api/go"
)

type VoskWord struct {
	Conf  float64 `json:"conf"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Word  string  `json:"word"`
}

type VoskResult struct {
	Result []VoskWord `json:"result"`
	Text   string     `json:"text"`
}

func VoskRecognizer(sampleRate float64, grammar string) *vosk.VoskRecognizer {
	modelPath := os.Getenv("VOSK_MODEL")
	model, err := vosk.NewModel(modelPath)
	if err != nil {
		log.Panic(err)
	}

	rec, err := vosk.NewRecognizerGrm(model, sampleRate, []byte(grammar))
	if err != nil {
		log.Panic(err)
	}
	rec.SetWords(1)

	return rec
}

func Recognize(rec *vosk.VoskRecognizer, data []byte, cb func(string)) {
	if rec.AcceptWaveform(data) != 0 {
		res := rec.FinalResult()

		// 2. Parse the JSON
		var response VoskResult
		if err := json.Unmarshal(res, &response); err != nil {
			fmt.Println("Error parsing JSON:", err)
			return
		}
		if len(response.Result) != 1 {
			return
		}
		if response.Result[0].Conf < 1 {
			return
		}
		cb(response.Text)
	}
}
