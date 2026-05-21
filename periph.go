package main

import (
	"fmt"
	"log"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/host/v3/rpi"
)

func showActive(active bool) {
	if active {
		rpi.P1_38.Out(gpio.High)
	} else {
		rpi.P1_38.Out(gpio.Low)
	}
}

func showSpeechEnabled(active bool) {
	if active {
		rpi.P1_32.Out(gpio.High)
	} else {
		rpi.P1_32.Out(gpio.Low)
	}
}

func fadingLED() {
	// Use GPIO 18 (Physical Pin 12) which supports Hardware PWM
	pin := rpi.P1_33

	// Define a high enough frequency so the LED doesn't flicker (e.g., 2 kHz)
	frequency := 2000 * physic.Hertz

	fmt.Println("Starting LED PWM fade loop. Press Ctrl+C to exit.")

	// Continuous loop fading up and down
	for {
		// Fade Up: Increase duty cycle from 0% to 100%
		for percent := 0; percent <= 100; percent += 5 {
			duty := gpio.Duty((percent * int(gpio.DutyMax)) / 100)
			if err := pin.PWM(duty, frequency); err != nil {
				log.Fatalf("Failed to set PWM: %v", err)
			}
			time.Sleep(40 * time.Millisecond)
		}

		// Fade Down: Decrease duty cycle from 100% to 0%
		for percent := 100; percent >= 0; percent -= 5 {
			duty := gpio.Duty((percent * int(gpio.DutyMax)) / 100)
			if err := pin.PWM(duty, frequency); err != nil {
				log.Fatalf("Failed to set PWM: %v", err)
			}
			time.Sleep(40 * time.Millisecond)
		}
	}
}
