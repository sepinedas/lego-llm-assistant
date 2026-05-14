package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/smallnest/ringbuffer"
	"google.golang.org/genai"
	"periph.io/x/host/v3"
)

const speechTimeout = 20 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var wg sync.WaitGroup

	rb := ringbuffer.New(1024 * 4096)

	if _, err := host.Init(); err != nil {
		log.Fatal(err)
	}

	Playback(rb)
	timer := time.NewTimer(0)

	rec := VoskRecognizer(InputSampleRate)
	isSpeechEnabled := false

	session, err := Session(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer session.Close()

	handleInputAudio := func(data []byte, framecount uint32) {
		if rb.IsEmpty() && isSpeechEnabled {
			err := session.SendRealtimeInput(genai.LiveRealtimeInput{
				Audio: &genai.Blob{
					MIMEType: "audio/pcm;rate=16000",
					Data:     data,
				},
			})
			if err != nil {
				log.Printf("Error sending audio: %v", err)
			}
		} else {
			Recognize(rec, IncreaseVolumeBytes(data, 2.5), func() {
				fmt.Println("Speech enabled.")
				isSpeechEnabled = true
				showSpeechEnabled(true)
				timer.Reset(speechTimeout)
			})
		}
	}
	Capture(handleInputAudio, InputSampleRate)

	showActive(true)
	defer showActive(false)
	defer showSpeechEnabled(false)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				fmt.Println("Stop session")
				return
			default:
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
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				if !rb.IsEmpty() {
					fmt.Println("Stop timer.")
					// 1. Stop the timer and check if it was already expired/stopped
					if !timer.Stop() {
						// 2. Drain the channel if it was not drained yet
						select {
						case <-timer.C:
						default:
						}
					}

					for !rb.IsEmpty() {
					}

					timer.Reset(speechTimeout)
				}
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return

			case <-timer.C:
				fmt.Println("Speech disabled.")
				isSpeechEnabled = false
				showSpeechEnabled(false)
			}
		}
	}()

	<-ctx.Done()
	fmt.Println("\nShutdown signal received. Waiting for workers to finish...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Channel to signal when all workers are done
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// 5. Select between workers finishing or the timeout expiring
	select {
	case <-done:
		fmt.Println("Main: all workers finished gracefully.")
	case <-shutdownCtx.Done():
		fmt.Println("Main: shutdown timed out, forcing exit.")
	}
}
