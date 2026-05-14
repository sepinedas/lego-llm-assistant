package main

import (
	"encoding/binary"
	"fmt"
	"math"
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

func Playback(playBuffer *ringbuffer.RingBuffer) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		fmt.Printf("LOG <%v>\n", message)
	})
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
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

func IncreaseVolumeBytes(b []byte, factor float64) []byte {
	// 16-bit audio requires 2 bytes per sample
	for i := 0; i < len(b); i += 2 {
		// Prevent out-of-bounds if the byte slice has an odd length
		if i+1 >= len(b) {
			break
		}

		// 1. Read 2 bytes into a uint16 and cast to signed int16
		sample := int16(binary.LittleEndian.Uint16(b[i : i+2]))

		// 2. Scale the sample amplitude
		newVal := float64(sample) * factor

		// 3. Clip values to prevent harsh digital distortion
		if newVal > math.MaxInt16 {
			newVal = math.MaxInt16
		} else if newVal < math.MinInt16 {
			newVal = math.MinInt16
		}

		// 4. Write back the updated sample as 2 bytes
		binary.LittleEndian.PutUint16(b[i:i+2], uint16(int16(newVal)))
	}
	return b
}
