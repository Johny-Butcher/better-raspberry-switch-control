package nscontroller

// This file implements the binary frame protocol used to carry Events between
// nsall --client and nsall --server with zero text serialization overhead.
//
// Frame layout (FrameSize = 17 bytes, all little-endian):
//
//	[0]      uint8   Action  (event action code)
//	[1..8]   uint64  Value   (float64 bit-cast, IEEE 754 LE)
//	[9..16]  uint64  UnixNano timestamp
//
// Because FrameSize < PIPE_BUF (4096 on Linux), every Write call is atomic
// on a Unix pipe — no additional framing or length prefix is required.

import (
	"encoding/binary"
	"io"
	"math"
	"time"
)

// FrameSize is the exact byte size of one encoded Event.
const FrameSize = 17

// EncodeFrame serialises ev into buf. buf must be at least FrameSize bytes.
func EncodeFrame(ev *Event, buf []byte) {
	buf[0] = byte(ev.Action)
	binary.LittleEndian.PutUint64(buf[1:9], math.Float64bits(ev.Value))
	binary.LittleEndian.PutUint64(buf[9:17], uint64(ev.Timestamp.UnixNano()))
}

// DecodeFrame deserialises a frame from buf (must be exactly FrameSize bytes).
func DecodeFrame(buf []byte) Event {
	return Event{
		Action:    Action(buf[0]),
		Value:     math.Float64frombits(binary.LittleEndian.Uint64(buf[1:9])),
		Timestamp: time.Unix(0, int64(binary.LittleEndian.Uint64(buf[9:17]))),
	}
}

// WriteFrame encodes ev and writes it as a single FrameSize-byte write to w.
// On a Linux pipe the write is guaranteed atomic (FrameSize < PIPE_BUF).
func WriteFrame(w io.Writer, ev *Event) error {
	var buf [FrameSize]byte
	EncodeFrame(ev, buf[:])
	_, err := w.Write(buf[:])
	return err
}

// ReadFrame reads exactly FrameSize bytes from r and returns a decoded Event.
// Returns io.EOF or io.ErrUnexpectedEOF when the stream closes cleanly.
func ReadFrame(r io.Reader) (Event, error) {
	var buf [FrameSize]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return Event{}, err
	}
	return DecodeFrame(buf[:]), nil
}

