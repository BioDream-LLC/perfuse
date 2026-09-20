// Package mllp implements HL7's Minimal Lower Layer Protocol, the framing used
// to carry v2 messages over TCP.
//
// The framing itself is three bytes: a start block, the message, then an end
// block and a carriage return. Everything difficult about it is what senders do
// wrong. This implementation is deliberately tolerant on read and strict on
// write: it resynchronises past junk between messages, enforces a size limit so
// a sender that never sends an end block cannot exhaust memory, and accepts a
// missing trailing carriage return.
package mllp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// MLLP framing bytes.
const (
	// StartBlock precedes every message.
	StartBlock = 0x0B
	// EndBlock follows every message.
	EndBlock = 0x1C
	// CarriageReturn follows the end block.
	CarriageReturn = 0x0D
)

// DefaultMaxMessageSize is the read limit applied when none is given. HL7 v2
// messages carrying embedded documents in OBX-5 can be large, so this is
// generous, but it is finite on purpose.
const DefaultMaxMessageSize = 16 << 20 // 16 MiB

var (
	// ErrTooLarge means a message exceeded the configured size limit. The
	// reader stays usable: it discards the oversized message and resynchronises.
	ErrTooLarge = errors.New("mllp: message exceeds the maximum size")

	// ErrEmptyMessage means a frame arrived containing nothing at all.
	ErrEmptyMessage = errors.New("mllp: frame contained no message")
)

// Reader reads MLLP frames from a stream.
type Reader struct {
	br      *bufio.Reader
	maxSize int

	// discarded counts bytes skipped while resynchronising, which is worth
	// reporting because it usually means the peer is misframing.
	discarded int64
}

// NewReader wraps r. A maxSize of zero applies DefaultMaxMessageSize.
func NewReader(r io.Reader, maxSize int) *Reader {
	if maxSize <= 0 {
		maxSize = DefaultMaxMessageSize
	}
	return &Reader{
		br:      bufio.NewReaderSize(r, 64<<10),
		maxSize: maxSize,
	}
}

// Discarded returns the number of bytes skipped while looking for a start
// block since the reader was created.
func (r *Reader) Discarded() int64 { return r.discarded }

// ReadMessage reads one message, returning the payload without framing bytes.
//
// Bytes appearing before a start block are discarded and counted rather than
// treated as an error: a peer that sends a stray newline between messages is
// misbehaving but not worth dropping a hospital feed over.
func (r *Reader) ReadMessage() ([]byte, error) {
	if err := r.sync(); err != nil {
		return nil, err
	}

	buf := make([]byte, 0, 4096)
	for {
		b, err := r.br.ReadByte()
		if err != nil {
			// A truncated frame is not a message. Returning what arrived so far
			// would hand a half-message to a clinical system.
			if errors.Is(err, io.EOF) && len(buf) > 0 {
				return nil, fmt.Errorf("mllp: stream ended %d bytes into a frame: %w", len(buf), io.ErrUnexpectedEOF)
			}
			return nil, err
		}

		switch b {
		case EndBlock:
			// The carriage return that follows is part of the framing. Some
			// senders omit it; accept the frame either way.
			if next, err := r.br.Peek(1); err == nil && next[0] == CarriageReturn {
				_, _ = r.br.ReadByte()
			}
			if len(buf) == 0 {
				return nil, ErrEmptyMessage
			}
			return buf, nil

		case StartBlock:
			// A start block inside a frame means the previous one was never
			// terminated. The partial message is unusable; keep the new frame.
			r.discarded += int64(len(buf))
			buf = buf[:0]

		default:
			if len(buf) >= r.maxSize {
				if err := r.drainFrame(); err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("%w (%d bytes)", ErrTooLarge, r.maxSize)
			}
			buf = append(buf, b)
		}
	}
}

// sync advances to just past the next start block.
func (r *Reader) sync() error {
	for {
		b, err := r.br.ReadByte()
		if err != nil {
			return err
		}
		if b == StartBlock {
			return nil
		}
		r.discarded++
	}
}

// drainFrame discards bytes up to and including the end of the current frame so
// that an oversized message does not desynchronise the stream.
func (r *Reader) drainFrame() error {
	for {
		b, err := r.br.ReadByte()
		if err != nil {
			return err
		}
		r.discarded++
		if b == EndBlock {
			if next, err := r.br.Peek(1); err == nil && next[0] == CarriageReturn {
				_, _ = r.br.ReadByte()
				r.discarded++
			}
			return nil
		}
	}
}

// Writer writes MLLP frames to a stream.
type Writer struct {
	w  io.Writer
	bw *bufio.Writer
}

// NewWriter wraps w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w, bw: bufio.NewWriterSize(w, 64<<10)}
}

// WriteMessage frames and writes one message, flushing before it returns.
//
// Any framing bytes already present on the payload are stripped, so handing this
// a message that came off the wire does not double-frame it.
func (w *Writer) WriteMessage(msg []byte) error {
	msg = TrimFraming(msg)
	if len(msg) == 0 {
		return ErrEmptyMessage
	}

	if err := w.bw.WriteByte(StartBlock); err != nil {
		return err
	}
	if _, err := w.bw.Write(msg); err != nil {
		return err
	}
	if _, err := w.bw.Write([]byte{EndBlock, CarriageReturn}); err != nil {
		return err
	}
	return w.bw.Flush()
}

// TrimFraming removes MLLP framing bytes from a payload if present.
//
// The trailing carriage return is only removed when it follows an end block.
// HL7 segments are themselves terminated by a carriage return, so stripping a
// trailing CR unconditionally would delete the terminator of the final segment
// and change the message.
func TrimFraming(msg []byte) []byte {
	if len(msg) > 0 && msg[0] == StartBlock {
		msg = msg[1:]
	}
	if n := len(msg); n >= 2 && msg[n-2] == EndBlock && msg[n-1] == CarriageReturn {
		return msg[:n-2]
	}
	if n := len(msg); n >= 1 && msg[n-1] == EndBlock {
		return msg[:n-1]
	}
	return msg
}

// Frame returns the message wrapped in MLLP framing.
func Frame(msg []byte) []byte {
	msg = TrimFraming(msg)
	out := make([]byte, 0, len(msg)+3)
	out = append(out, StartBlock)
	out = append(out, msg...)
	out = append(out, EndBlock, CarriageReturn)
	return out
}
