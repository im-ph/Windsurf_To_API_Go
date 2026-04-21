// Package pbenc is a zero-dependency protobuf wire-format codec. It provides
// just enough primitives to build and parse the Windsurf language-server
// messages described in src/windsurf.js — we never need full schema support.
//
// Wire types:
//
//	0 = Varint    (int32, uint64, bool, enum)
//	1 = Fixed64   (double, fixed64)
//	2 = LenDelim  (string, bytes, embedded messages)
//	5 = Fixed32   (float, fixed32)
package pbenc

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ─── Varint ────────────────────────────────────────────────

// AppendVarint appends an unsigned varint to dst.
func AppendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

// DecodeVarint decodes a varint starting at buf. Returns value and byte count.
func DecodeVarint(buf []byte) (uint64, int, error) {
	var v uint64
	var shift uint
	for i, b := range buf {
		if i >= 10 {
			return 0, 0, errors.New("pbenc: varint overflow")
		}
		v |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return v, i + 1, nil
		}
		shift += 7
	}
	return 0, 0, errors.New("pbenc: truncated varint")
}

// ─── Field writers ─────────────────────────────────────────

func tag(field int, wire int) uint64 { return uint64(field)<<3 | uint64(wire) }

// Varint field (wire type 0).
func AppendVarintField(dst []byte, field int, v uint64) []byte {
	dst = AppendVarint(dst, tag(field, 0))
	return AppendVarint(dst, v)
}

// String field (wire type 2). Empty strings still emit the field header — the
// JS original (writeStringField) treats "" as present but ignores undefined.
// We skip empty so JS-equivalent Buffer.alloc(0) behaviour is preserved.
func AppendStringField(dst []byte, field int, s string) []byte {
	if s == "" {
		return dst
	}
	dst = AppendVarint(dst, tag(field, 2))
	dst = AppendVarint(dst, uint64(len(s)))
	return append(dst, s...)
}

// Bytes field (wire type 2).
func AppendBytesField(dst []byte, field int, b []byte) []byte {
	if len(b) == 0 {
		return dst
	}
	dst = AppendVarint(dst, tag(field, 2))
	dst = AppendVarint(dst, uint64(len(b)))
	return append(dst, b...)
}

// Message field (wire type 2). Skips zero-length messages to match JS.
func AppendMessageField(dst []byte, field int, msg []byte) []byte {
	if len(msg) == 0 {
		return dst
	}
	dst = AppendVarint(dst, tag(field, 2))
	dst = AppendVarint(dst, uint64(len(msg)))
	return append(dst, msg...)
}

// Bool field (wire type 0), emitted only when true — matches writeBoolField.
func AppendBoolField(dst []byte, field int, v bool) []byte {
	if !v {
		return dst
	}
	return AppendVarintField(dst, field, 1)
}

// Fixed64 field (wire type 1).
func AppendFixed64Field(dst []byte, field int, v uint64) []byte {
	dst = AppendVarint(dst, tag(field, 1))
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	return append(dst, b[:]...)
}

// ─── Parser ────────────────────────────────────────────────

// Field is a raw parsed protobuf field. For wireType 2 Value is the inner
// bytes; for varint/fixed it's the numeric value.
type Field struct {
	Num      int
	WireType int
	Value    []byte // wireType 2 only
	Varint   uint64 // wireType 0 only
	Fixed    uint64 // wireType 1 / 5
}

// Parse walks buf and returns every field it finds. Malformed tails raise
// an error.
func Parse(buf []byte) ([]Field, error) {
	var out []Field
	for pos := 0; pos < len(buf); {
		t, n, err := DecodeVarint(buf[pos:])
		if err != nil {
			return nil, fmt.Errorf("parse tag: %w", err)
		}
		pos += n
		num := int(t >> 3)
		wire := int(t & 7)

		switch wire {
		case 0:
			v, n, err := DecodeVarint(buf[pos:])
			if err != nil {
				return nil, fmt.Errorf("parse varint field=%d: %w", num, err)
			}
			out = append(out, Field{Num: num, WireType: 0, Varint: v})
			pos += n
		case 1:
			if pos+8 > len(buf) {
				return nil, errors.New("pbenc: truncated fixed64")
			}
			out = append(out, Field{Num: num, WireType: 1, Fixed: binary.LittleEndian.Uint64(buf[pos : pos+8])})
			pos += 8
		case 2:
			l, n, err := DecodeVarint(buf[pos:])
			if err != nil {
				return nil, fmt.Errorf("parse len field=%d: %w", num, err)
			}
			pos += n
			// Reject lengths that exceed the buffer or would overflow int on any
			// supported platform (protobuf fields are at most 2 GB per the spec).
			const maxFieldBytes = 1 << 31
			if l > maxFieldBytes || pos+int(l) > len(buf) {
				return nil, errors.New("pbenc: truncated length-delimited field")
			}
			out = append(out, Field{Num: num, WireType: 2, Value: buf[pos : pos+int(l)]})
			pos += int(l)
		case 5:
			if pos+4 > len(buf) {
				return nil, errors.New("pbenc: truncated fixed32")
			}
			out = append(out, Field{Num: num, WireType: 5, Fixed: uint64(binary.LittleEndian.Uint32(buf[pos : pos+4]))})
			pos += 4
		default:
			return nil, fmt.Errorf("pbenc: unknown wire type %d at field %d", wire, num)
		}
	}
	return out, nil
}

// Get returns the first field matching num (optionally constrained by wire).
func Get(fields []Field, num int, wire int) *Field {
	for i := range fields {
		if fields[i].Num == num && (wire < 0 || fields[i].WireType == wire) {
			return &fields[i]
		}
	}
	return nil
}

// All returns every field matching num.
func All(fields []Field, num int) []*Field {
	var out []*Field
	for i := range fields {
		if fields[i].Num == num {
			out = append(out, &fields[i])
		}
	}
	return out
}
