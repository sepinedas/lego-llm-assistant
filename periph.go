package main

import (
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/host/v3/rpi"
)

func showCommandEnabled(active bool) {
	if active {
		rpi.P1_32.Out(gpio.High)
	} else {
		rpi.P1_32.Out(gpio.Low)
	}
}

func showSpeechEnabled(active bool) {
	if active {
		rpi.P1_33.Out(gpio.High)
	} else {
		rpi.P1_33.Out(gpio.Low)
	}
}
