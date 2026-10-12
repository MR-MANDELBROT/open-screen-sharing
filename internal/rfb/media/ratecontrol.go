package media

import "math"

// AVConference VCRateControlOWRDEstimator uses short-average weights 0.9/0.1
// and long-average weights 0.9999/0.0001 (AVConference 2235.63.5.2,
// macOS 27.0.1 26A434, UUID 93D8C8C0-8B9C-3A5E-A47D-01B5FAF73E1B,
// -[VCRateControlOWRDEstimator calculateOWRDWithSendTime:receiveTime:roundTripTime:] 0x1c0f14340..0x1c0f1436c;
// AVConference 2260.14.1, macOS 27.2 26B5101f, UUID
// EDFB99EF-2F5E-35DD-8E63-089CBD102EFE,
// -[VCRateControlOWRDEstimator calculateOWRDWithSendTime:receiveTime:roundTripTime:] 0x1c058efd8..0x1c058f004).
type owrdEstimator struct {
	send         owrdClock
	receive      owrdClock
	firstSend    float64
	firstReceive float64
	short        float64
	long         float64
	value        float64
	spurious     int
}

type owrdClock struct {
	base       uint32
	last       uint32
	wraps      uint64
	seen       uint32
	step       float64
	spike      bool
	disorder   int
	disorderAt uint32
}

const (
	owrdSpuriousLag   = 30
	owrdResetAfter    = 30
	owrdSpikeFactor   = 10
	owrdSettledFactor = 1.2
)

func (e *owrdEstimator) sample(timestamp uint32, clockRate float64, arrivalMilliseconds uint32) {
	send := e.send.relative(e, timestamp, clockRate)
	receive := e.receive.relative(e, arrivalMilliseconds, 1000)
	if send < 0 || receive < 0 {
		return
	}
	if e.firstSend == 0 {
		e.firstSend = send
	}
	if e.firstReceive == 0 {
		e.firstReceive = receive
	}
	lag := (receive - e.firstReceive) - (send - e.firstSend)
	if e.short == 0 || e.long == 0 {
		e.short, e.long = lag, lag
	}
	if lag-e.short > owrdSpuriousLag || e.long-lag > owrdSpuriousLag {
		if e.send.spike || e.receive.spike {
			e.spurious = 0
			return
		}
		e.spurious++
		if e.spurious > owrdResetAfter {
			e.reset()
		}
		return
	}
	e.spurious = 0
	e.long = 0.9999*e.long + 0.0001*lag
	e.short = 0.9*e.short + 0.1*lag
	e.value = e.short - e.long
	if e.value < 0 {
		e.long = e.short
		e.value = 0
	}
}

// AVConference resetOWRDEstimation preserves the OWRD result and both
// timestamp wrap counters (AVConference 2235.63.5.2, macOS 27.0.1 26A434,
// UUID 93D8C8C0-8B9C-3A5E-A47D-01B5FAF73E1B,
// -[VCRateControlOWRDEstimator resetOWRDEstimation] 0x1c0f158c4..0x1c0f158dc;
// AVConference 2260.14.1, macOS 27.2 26B5101f, UUID
// EDFB99EF-2F5E-35DD-8E63-089CBD102EFE,
// -[VCRateControlOWRDEstimator resetOWRDEstimation] 0x1c059055c..0x1c0590574).
func (e *owrdEstimator) reset() {
	for _, clock := range []*owrdClock{&e.send, &e.receive} {
		clock.base, clock.last, clock.seen, clock.step = 0, 0, 0, 0
	}
	e.firstSend, e.firstReceive, e.short, e.long = 0, 0, 0, 0
}

func (c *owrdClock) relative(e *owrdEstimator, timestamp uint32, rate float64) float64 {
	if c.base == 0 {
		c.base = timestamp
	}
	relative := timestamp - c.base
	if relative == c.last {
		return -1
	}
	if c.dropped(e, timestamp) {
		return -2
	}
	if relative-c.last <= 0x7ffffffe {
		if relative < c.last {
			c.wraps++
		}
		c.last = relative
	}
	// AVConference counts each 32-bit timestamp wrap as 2^32-1
	// (AVConference 2235.63.5.2, macOS 27.0.1 26A434, UUID
	// 93D8C8C0-8B9C-3A5E-A47D-01B5FAF73E1B,
	// -[VCRateControlOWRDEstimator relativeSendTimeWithTimestamp:timestampRate:] 0x1c0f13af8..0x1c0f13b20,
	// -[VCRateControlOWRDEstimator relativeReceiveTimeWithTimestamp:timestampRate:] 0x1c0f13dc0..0x1c0f13de8;
	// AVConference 2260.14.1, macOS 27.2 26B5101f, UUID
	// EDFB99EF-2F5E-35DD-8E63-089CBD102EFE,
	// -[VCRateControlOWRDEstimator relativeSendTimeWithTimestamp:timestampRate:] 0x1c058e790..0x1c058e7b8,
	// -[VCRateControlOWRDEstimator relativeReceiveTimeWithTimestamp:timestampRate:] 0x1c058ea58..0x1c058ea80).
	return float64(c.wraps*math.MaxUint32+uint64(relative)) / rate
}

func (c *owrdClock) dropped(e *owrdEstimator, timestamp uint32) bool {
	if c.seen == 0 {
		c.seen = timestamp
		return false
	}
	step := timestamp - c.seen
	dropped := false
	switch {
	case c.step <= 0:
	case step >= 0x7fffffff:
		dropped = true
		c.outOfOrder(e, timestamp)
	case step > saturatingUint32(c.step*owrdSpikeFactor):
		c.spike = true
		c.disorder = 0
		dropped = true
		c.outOfOrder(e, timestamp)
	default:
		if c.spike && step < saturatingUint32(c.step*owrdSettledFactor) {
			e.reset()
		}
		c.disorder = 0
		c.seen = timestamp
		c.spike = false
	}
	if c.step == 0 {
		c.step = float64(step)
	} else {
		c.step = 0.9*c.step + 0.1*float64(step)
	}
	return dropped
}

func (c *owrdClock) outOfOrder(e *owrdEstimator, timestamp uint32) {
	if timestamp-c.disorderAt <= 0x7ffffffe {
		c.disorder++
	} else {
		c.disorder = 0
	}
	c.disorderAt = timestamp
	if c.disorder > owrdResetAfter {
		e.reset()
	}
}

func saturatingUint32(value float64) uint32 {
	switch {
	case !(value > 0):
		return 0
	case value >= math.MaxUint32:
		return math.MaxUint32
	default:
		return uint32(value)
	}
}
