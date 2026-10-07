package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

const gestureStep = 16 * time.Millisecond

func runGestureAction(client *rfb.Client, verb, argument string, wait func(time.Duration) error) (bool, error) {
	switch verb {
	case "dblclick":
		numbers, err := parseNumbers(argument, 2, strconv.Atoi)
		if err != nil {
			return true, err
		}
		for range 2 {
			err = client.SendPointer(numbers[0], numbers[1], rfb.ButtonLeft)
			if err == nil {
				err = client.SendPointer(numbers[0], numbers[1], 0)
			}
			if err != nil {
				return true, err
			}
		}
		return true, nil
	case "drag":
		numbers, err := parseNumbers(argument, 4, strconv.Atoi)
		if err != nil {
			return true, err
		}
		return true, drag(client, numbers, wait)
	case "pinch", "rotate":
		values, err := parseNumbers(argument, 4, func(value string) (float64, error) {
			return strconv.ParseFloat(value, 64)
		})
		if err != nil {
			return true, err
		}
		send := client.SendMagnify
		if verb == "rotate" {
			send = client.SendRotate
		}
		return true, runAmountGesture(send, int(values[0]), int(values[1]), int(values[2]), values[3], wait)
	case "smart-magnify", "lookup":
		numbers, err := parseNumbers(argument, 2, strconv.Atoi)
		if err != nil {
			return true, err
		}
		if verb == "lookup" {
			return true, client.SendLookup(numbers[0], numbers[1])
		}
		return true, client.SendSmartMagnify(numbers[0], numbers[1])
	case "swipe":
		parts := strings.Split(argument, ",")
		if len(parts) != 3 {
			return true, fmt.Errorf("%q is not X,Y,DIRECTION", argument)
		}
		numbers, err := parseNumbers(strings.Join(parts[:2], ","), 2, strconv.Atoi)
		if err != nil {
			return true, err
		}
		directions := map[string]rfb.SwipeDirection{"back": rfb.SwipeBack, "forward": rfb.SwipeForward, "up": rfb.SwipeUp, "down": rfb.SwipeDown}
		direction, found := directions[parts[2]]
		if !found {
			return true, fmt.Errorf("unknown swipe direction %q", parts[2])
		}
		return true, client.SendSwipe(numbers[0], numbers[1], direction)
	case "flick":
		values, err := parseNumbers(argument, 4, func(value string) (float64, error) {
			return strconv.ParseFloat(value, 64)
		})
		if err != nil {
			return true, err
		}
		return true, flick(client, values, wait)
	case "system":
		command, err := rfb.ParseSystemCommand(argument)
		if err != nil {
			return true, err
		}
		return true, client.SendSystemCommand(command)
	case "assist":
		parts := strings.Split(argument, ",")
		numbers, err := parseNumbers(strings.Join(parts[:min(len(parts), 3)], ","), 2, strconv.Atoi)
		if err != nil {
			return true, err
		}
		pointer := rfb.AssistPointerHighlight
		if len(numbers) > 2 {
			pointer = uint32(numbers[2])
		}
		pressed := len(parts) > 3 && parts[3] == "pressed"
		return true, client.SetAssistCursor(numbers[0], numbers[1], pointer, pressed)
	case "assist-off":
		return true, client.HideAssistCursor()
	default:
		return false, nil
	}
}

func drag(client *rfb.Client, numbers []int, wait func(time.Duration) error) error {
	const steps = 20
	err := client.SendPointer(numbers[0], numbers[1], rfb.ButtonLeft)
	for i := 1; i <= steps && err == nil; i++ {
		err = wait(gestureStep)
		if err == nil {
			x := numbers[0] + (numbers[2]-numbers[0])*i/steps
			y := numbers[1] + (numbers[3]-numbers[1])*i/steps
			err = client.SendPointer(x, y, rfb.ButtonLeft)
		}
	}
	if err != nil {
		return err
	}
	return client.SendPointer(numbers[2], numbers[3], 0)
}

func runAmountGesture(send func(x, y int, amount float64, phase rfb.GesturePhase) error, x, y, steps int, amount float64, wait func(time.Duration) error) error {
	phase := rfb.PhaseBegan
	for i := 0; i < steps; i++ {
		err := send(x, y, amount, phase)
		if err == nil {
			err = wait(gestureStep)
		}
		if err != nil {
			return err
		}
		phase = rfb.PhaseChanged
	}
	return send(x, y, 0, rfb.PhaseEnded)
}

func flick(client *rfb.Client, values []float64, wait func(time.Duration) error) error {
	const steps = 10
	x, y := int(values[0]), int(values[1])
	deltaY, velocityY := values[2], values[3]
	var deltaX, velocityX float64
	if len(values) > 5 {
		deltaX, velocityX = values[4], values[5]
	}
	err := client.SendScrollPhase(x, y, 0, 0, rfb.PhaseMayBegin)
	phase := rfb.PhaseBegan
	for i := 0; i < steps && err == nil; i++ {
		err = wait(gestureStep)
		if err == nil {
			err = client.SendScrollPhase(x, y, deltaX/steps, deltaY/steps, phase)
		}
		phase = rfb.PhaseChanged
	}
	if err == nil {
		err = client.SendScrollPhase(x, y, 0, 0, rfb.PhaseEnded)
	}
	if err != nil || (velocityX == 0 && velocityY == 0) {
		return err
	}
	return client.StartScrollMomentum(x, y, velocityX, velocityY)
}
