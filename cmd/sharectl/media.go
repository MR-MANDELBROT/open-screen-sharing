package main

import (
	"encoding/binary"
	"log/slog"
	"os"
	"sync"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
	E "github.com/sagernet/sing/common/exceptions"
)

type mediaRecorder struct {
	logger *slog.Logger
	file   *os.File

	access       sync.Mutex
	formats      int
	accessUnits  int
	syncUnits    int
	videoBytes   int
	audioPackets int
	audioLost    int
}

func newMediaRecorder(logger *slog.Logger, path string) (*mediaRecorder, error) {
	recorder := &mediaRecorder{logger: logger}
	if path != "" {
		file, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		recorder.file = file
	}
	return recorder, nil
}

var startCode = []byte{0, 0, 0, 1}

func (r *mediaRecorder) VideoFormat(format media.VideoFormat) {
	r.access.Lock()
	defer r.access.Unlock()
	r.formats++
	color := format.Color
	r.logger.Info("video format", "codec", format.Codec.String(), "parameter-sets", len(format.ParameterSets), "bands", format.Bands,
		"bit-depth", color.BitDepth, "chroma", color.ChromaFormat, "full-range", color.FullRange,
		"primaries", color.Primaries, "transfer", color.Transfer, "matrix", color.Matrix, "hdr", color.HDR())
	if r.file == nil {
		return
	}
	for _, parameterSet := range format.ParameterSets {
		r.write(startCode)
		r.write(parameterSet)
	}
}

func (r *mediaRecorder) VideoAccessUnit(unit media.VideoAccessUnit) {
	r.access.Lock()
	defer r.access.Unlock()
	r.accessUnits++
	r.videoBytes += len(unit.Data)
	if unit.Sync {
		r.syncUnits++
	}
	if r.file == nil {
		return
	}
	data := unit.Data
	for len(data) >= 4 {
		length := int(binary.BigEndian.Uint32(data))
		if 4+length > len(data) {
			break
		}
		r.write(startCode)
		r.write(data[4 : 4+length])
		data = data[4+length:]
	}
}

func (r *mediaRecorder) write(data []byte) {
	_, err := r.file.Write(data)
	if err != nil {
		fatal(E.Cause(err, "write media recording"))
	}
}

func (r *mediaRecorder) AudioFormat(format media.AudioFormat) {
	r.logger.Info("audio format", "sample-rate", format.SampleRate, "channels", format.Channels, "frames", format.FramesPerPacket)
}

func (r *mediaRecorder) AudioPacket(packet media.AudioPacket) {
	r.access.Lock()
	r.audioPackets++
	r.audioLost += packet.Lost
	r.access.Unlock()
}

func (r *mediaRecorder) StreamStopped(err error) {
	r.logger.Info("media stream stopped", "error", err)
}

func (r *mediaRecorder) close() {
	r.access.Lock()
	defer r.access.Unlock()
	r.logger.Info("media statistics", "formats", r.formats, "access-units", r.accessUnits, "sync-units", r.syncUnits,
		"video-bytes", r.videoBytes, "audio-packets", r.audioPackets, "audio-lost", r.audioLost)
	if r.file != nil {
		r.file.Close()
	}
}
