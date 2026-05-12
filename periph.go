package main

import (
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/host/v3/rpi"
)

func showActive(active bool) {
	if active {
		rpi.P1_38.Out(gpio.High)
	} else {
		rpi.P1_38.Out(gpio.Low)
	}
}
