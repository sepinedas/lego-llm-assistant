package main

import (
	"fmt"
	"os"

	"github.com/gen2brain/malgo"
	"github.com/smallnest/ringbuffer"
)

const (
	InputSampleRate  = 16000
	OutputSampleRate = 24000
)

func getRecorderConfig(sampleRate uint32) malgo.DeviceConfig {
	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = 1
	deviceConfig.SampleRate = sampleRate
	deviceConfig.Alsa.NoMMap = 1

	return deviceConfig
}

func getPlayerConfig() malgo.DeviceConfig {
	deviceConfig := malgo.DefaultDeviceConfig(malgo.Playback)
	deviceConfig.Playback.Format = malgo.FormatS16
	deviceConfig.Playback.Channels = 1
	deviceConfig.SampleRate = 24000
	deviceConfig.Alsa.NoMMap = 1

	return deviceConfig
}

func Capture(cb func([]byte, uint32), sampleRate uint32) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		fmt.Printf("LOG <%v>\n", message)
	})
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	onRecvFrames := func(pSample2, pSample []byte, framecount uint32) {
		if len(pSample) > 0 {
			cb(pSample, framecount)
		}
	}

	captureCallbacks := malgo.DeviceCallbacks{
		Data: onRecvFrames,
	}

	device, err := malgo.InitDevice(ctx.Context, getRecorderConfig(sampleRate), captureCallbacks)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = device.Start()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("Recording...")
}

func Playback(playBuffer *ringbuffer.RingBuffer, isCommandOpen *bool) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		fmt.Printf("LOG <%v>\n", message)
	})
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	onSendFrames := func(pOutput, nil []byte, framecount uint32) {
		// Read only as much as malgo needs for this frame
		if !(*isCommandOpen) {
			n, _ := playBuffer.Read(pOutput)

			// Fill remaining with silence if buffer is empty to avoid noise
			if n < len(pOutput) {
				for i := n; i < len(pOutput); i++ {
					pOutput[i] = 0
				}
			}
		}
	}

	playbackCallbacks := malgo.DeviceCallbacks{
		Data: onSendFrames,
	}

	device, err := malgo.InitDevice(ctx.Context, getPlayerConfig(), playbackCallbacks)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = device.Start()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("Playing...")
}
