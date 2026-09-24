package disc

import (
	"encoding/binary"
	"errors"
)

// errTruncated is the error of every out-of-range read: the data ends early or a length or
// offset field points outside it.
var errTruncated = errors.New("truncated or malformed data")

// reader is a bounds-checked big-endian reader over an in-memory buffer. The first
// out-of-range access sets err (sticky) and every later read returns zero values, so a parser
// can read a whole structure and check err once: no read can panic or leave the buffer.
// Length-prefixed blocks are read through sub, which confines the block's parser to the
// declared length and leaves the parent positioned right after the block (the "seek to
// start + length" rule of the Blu-ray formats).
type reader struct {
	b   []byte
	off int
	err error
}

func newReader(b []byte) *reader { return &reader{b: b} }

func (r *reader) fail() {
	if r.err == nil {
		r.err = errTruncated
	}
}

// avail is the number of unread bytes (0 after an error).
func (r *reader) avail() int {
	if r.err != nil {
		return 0
	}
	return len(r.b) - r.off
}

// take returns the next n bytes (sharing the buffer) and advances past them.
func (r *reader) take(n int) ([]byte, bool) {
	if r.err != nil {
		return nil, false
	}
	if n < 0 || n > len(r.b)-r.off {
		r.fail()
		return nil, false
	}
	s := r.b[r.off : r.off+n : r.off+n]
	r.off += n
	return s, true
}

func (r *reader) skip(n int) { _, _ = r.take(n) }

func (r *reader) u8() uint8 {
	s, ok := r.take(1)
	if !ok {
		return 0
	}
	return s[0]
}

func (r *reader) u16() uint16 {
	s, ok := r.take(2)
	if !ok {
		return 0
	}
	return binary.BigEndian.Uint16(s)
}

func (r *reader) u32() uint32 {
	s, ok := r.take(4)
	if !ok {
		return 0
	}
	return binary.BigEndian.Uint32(s)
}

// str reads n bytes as a string (no validation; callers check what they need).
func (r *reader) str(n int) string {
	s, ok := r.take(n)
	if !ok {
		return ""
	}
	return string(s)
}

// seek moves to the absolute offset off (0 ≤ off ≤ len).
func (r *reader) seek(off int64) {
	if r.err != nil {
		return
	}
	if off < 0 || off > int64(len(r.b)) {
		r.fail()
		return
	}
	r.off = int(off)
}

// sub returns a reader over the next n bytes and advances r past them. When fewer than n
// bytes remain, both r and the returned reader are in the error state.
func (r *reader) sub(n int) *reader {
	s, ok := r.take(n)
	if !ok {
		return &reader{err: errTruncated}
	}
	return &reader{b: s}
}
