package main

import (
	"fmt"
	"os"

	"github.com/gen2brain/malgo"
	"github.com/smallnest/ringbuffer"
)

type PlayerDevice struct {
	device  *malgo.Device
	context malgo.Context
	buffer  *ringbuffer.RingBuffer
}
type RecordDevice struct {
	device  *malgo.Device
	c       chan []byte
	context malgo.Context
}

func getRecorderConfig() malgo.DeviceConfig {
	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = 1
	deviceConfig.SampleRate = 16000
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

func NewRecorder(c chan []byte, ctx malgo.Context) *RecordDevice {
	onRecvFrames := func(pSample2, pSample []byte, framecount uint32) {
		if len(pSample) > 0 {
			c <- pSample
		}
	}

	captureCallbacks := malgo.DeviceCallbacks{
		Data: onRecvFrames,
	}

	device, err := malgo.InitDevice(ctx, getRecorderConfig(), captureCallbacks)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	d := &RecordDevice{device, c, ctx}

	return d
}

func (recorder RecordDevice) record() {
	err := recorder.device.Start()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("Recording...")
}

func NewPlayer(playBuffer *ringbuffer.RingBuffer, ctx malgo.Context) *PlayerDevice {
	onSendFrames := func(pOutput, nil []byte, framecount uint32) {
		// Read only as much as malgo needs for this frame
		n, _ := playBuffer.Read(pOutput)

		// Fill remaining with silence if buffer is empty to avoid noise
		if n < len(pOutput) {
			for i := n; i < len(pOutput); i++ {
				pOutput[i] = 0
			}
		}
	}

	playbackCallbacks := malgo.DeviceCallbacks{
		Data: onSendFrames,
	}

	device, err := malgo.InitDevice(ctx, getPlayerConfig(), playbackCallbacks)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	d := &PlayerDevice{device, ctx, playBuffer}

	return d
}

func (player PlayerDevice) start() {
	err := player.device.Start()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("Playing...")
}
