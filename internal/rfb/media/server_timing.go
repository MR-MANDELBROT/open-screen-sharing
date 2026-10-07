package media

import (
	"encoding/binary"
	"time"
)

const (
	timingLogInterval = 5 * time.Second
	deviceClockLimit  = 10 * time.Second
)

type serverTiming struct {
	since          time.Time
	frameStarted   bool
	frameTimestamp uint32
	frameBytes     int
	frameArrival   time.Time
	deviceClock    bool
	lastLog        time.Time
	interval       timingInterval
}

type timingInterval struct {
	frames            int
	frameDelay        durationRange
	largestFrame      int
	largestFrameDelay time.Duration
	reportDelay       durationRange
	queueWait         durationRange
	sendErrors        int
	lastSendError     error
	clientDelay       time.Duration
}

type durationRange struct {
	count         int
	sum, min, max time.Duration
}

func (r *durationRange) add(d time.Duration) {
	if r.count == 0 || d < r.min {
		r.min = d
	}
	if r.count == 0 || d > r.max {
		r.max = d
	}
	r.count++
	r.sum += d
}

func (r *durationRange) average() time.Duration {
	if r.count == 0 {
		return 0
	}
	return r.sum / time.Duration(r.count)
}

func timeFromNTP(ntp uint64) time.Time {
	const secondsFrom1900To1970 = 2208988800
	nanoseconds := (ntp & 0xffffffff) * 1_000_000_000 >> 32
	return time.Unix(int64(ntp>>32)-secondsFrom1900To1970, int64(nanoseconds))
}

func (l *serverLeg) startTimingLocked(now time.Time) {
	l.timing.since = now
	l.timing.frameStarted = false
}

func (l *serverLeg) timeReportLocked(ntp uint64, arrived time.Time) {
	delay := arrived.Sub(timeFromNTP(ntp))
	l.timing.interval.reportDelay.add(delay)
	l.timing.deviceClock = delay > -deviceClockLimit && delay < deviceClockLimit
}

func (l *serverLeg) timePacketLocked(header rtpPacket, size int, arrived, now time.Time) {
	t := &l.timing
	t.interval.queueWait.add(now.Sub(arrived))
	if l.active == nil || header.ssrc != l.active.ssrc {
		return
	}
	if t.frameStarted && header.timestamp == t.frameTimestamp {
		t.frameBytes += size
		t.frameArrival = arrived
		return
	}
	if t.frameStarted {
		l.endFrameLocked()
	}
	t.frameStarted = true
	t.frameTimestamp, t.frameBytes, t.frameArrival = header.timestamp, size, arrived
}

func (l *serverLeg) endFrameLocked() {
	t := &l.timing
	t.interval.frames++
	report := l.deviceReports[l.active.ssrc]
	if report == nil || report.receivedAt.Before(t.since) {
		return
	}
	elapsed := time.Duration(float64(int32(t.frameTimestamp-report.timestamp)) / l.clockRate * float64(time.Second))
	delay := t.frameArrival.Sub(timeFromNTP(report.ntp).Add(elapsed))
	interval := &t.interval
	interval.frameDelay.add(delay)
	if t.frameBytes > interval.largestFrame {
		interval.largestFrame, interval.largestFrameDelay = t.frameBytes, delay
	}
}

// AVConference packs the RCTL delay in units of 1/8192 s
// (macOS 26A434 and 26B5101f).
func (l *serverLeg) timeClientReportLocked(report []byte) {
	delay := time.Duration(binary.BigEndian.Uint16(report[26:])) * time.Second / 8192
	l.timing.interval.clientDelay = max(l.timing.interval.clientDelay, delay)
}

func (l *serverLeg) takeTimingLocked(now time.Time) ([]any, bool) {
	t := &l.timing
	if now.Sub(t.lastLog) < timingLogInterval {
		return nil, false
	}
	t.lastLog = now
	interval := t.interval
	t.interval = timingInterval{}
	if interval.frames == 0 && interval.sendErrors == 0 {
		return nil, false
	}
	round := func(d time.Duration) time.Duration { return d.Round(100 * time.Microsecond) }
	frameDelay := interval.frameDelay
	attributes := []any{"frames", interval.frames}
	switch {
	case frameDelay.count == 0:
	case t.deviceClock:
		attributes = append(attributes,
			"frame_delay_min", round(frameDelay.min),
			"frame_delay_avg", round(frameDelay.average()),
			"frame_delay_max", round(frameDelay.max),
			"largest_frame", interval.largestFrame,
			"largest_frame_delay", round(interval.largestFrameDelay))
	default:
		attributes = append(attributes,
			"frame_delay_above_min_avg", round(frameDelay.average()-frameDelay.min),
			"frame_delay_above_min_max", round(frameDelay.max-frameDelay.min),
			"largest_frame", interval.largestFrame,
			"largest_frame_delay_above_min", round(interval.largestFrameDelay-frameDelay.min))
	}
	if interval.reportDelay.count > 0 {
		if t.deviceClock {
			attributes = append(attributes, "report_delay_max", round(interval.reportDelay.max))
		} else {
			attributes = append(attributes, "device_clock_offset", round(interval.reportDelay.max))
		}
	}
	attributes = append(attributes,
		"queue_wait_avg", round(interval.queueWait.average()),
		"queue_wait_max", round(interval.queueWait.max),
		"send_errors", interval.sendErrors)
	if interval.lastSendError != nil {
		attributes = append(attributes, "send_error", interval.lastSendError.Error())
	}
	return append(attributes, "client_rtt", round(l.clientRTT), "client_delay_max", round(interval.clientDelay)), true
}
