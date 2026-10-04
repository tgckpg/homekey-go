// SPDX-License-Identifier: Apache-2.0
package provision

import "fmt"

const MaxMessage = 4096

type Fields map[byte][]byte

// DecodeTLV accepts a single TLV8 object. Adjacent 255-byte fragments are
// reassembled; ambiguous duplicate fields and list separators are rejected.
func DecodeTLV(b []byte) (Fields, error) {
	if len(b) > MaxMessage {
		return nil, fmt.Errorf("TLV exceeds limit")
	}
	out := make(Fields)
	var previous byte
	previousLen := -1
	for len(b) != 0 {
		if len(b) < 2 {
			return nil, fmt.Errorf("truncated TLV header")
		}
		t, n := b[0], int(b[1])
		b = b[2:]
		if t == 0xff || len(b) < n {
			return nil, fmt.Errorf("invalid TLV field")
		}
		if _, exists := out[t]; exists && (previous != t || previousLen != 255) {
			return nil, fmt.Errorf("duplicate TLV field")
		}
		out[t] = append(out[t], b[:n]...)
		// Preserve the presence of an explicitly empty field.
		if out[t] == nil {
			out[t] = []byte{}
		}
		previous, previousLen, b = t, n, b[n:]
	}
	return out, nil
}

func TLV(t byte, value []byte) []byte {
	var out []byte
	for len(value) > 255 {
		out = append(out, t, 255)
		out = append(out, value[:255]...)
		value = value[255:]
	}
	out = append(out, t, byte(len(value)))
	return append(out, value...)
}

func one(f Fields, t byte) (byte, error) {
	if len(f[t]) != 1 {
		return 0, fmt.Errorf("missing or invalid one-byte field %d", t)
	}
	return f[t][0], nil
}
