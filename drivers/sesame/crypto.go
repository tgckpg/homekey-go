// SPDX-License-Identifier: Apache-2.0
package sesame

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"math"
)

// RFC 4493 AES-CMAC (full 128-bit output).
func cmac(key, data []byte) ([]byte, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	double := func(x []byte) []byte {
		r := make([]byte, 16)
		var c byte
		for i := 15; i >= 0; i-- {
			r[i] = x[i]<<1 | c
			c = x[i] >> 7
		}
		if c != 0 {
			r[15] ^= 0x87
		}
		return r
	}
	l := make([]byte, 16)
	b.Encrypt(l, l)
	k1 := double(l)
	k2 := double(k1)
	n := (len(data) + 15) / 16
	if n == 0 {
		n = 1
	}
	last := make([]byte, 16)
	copy(last, data[(n-1)*16:])
	k := k1
	if len(data) == 0 || len(data)%16 != 0 {
		last[len(data)%16] = 0x80
		k = k2
	}
	state := make([]byte, 16)
	for i := 0; i < n-1; i++ {
		for j := range state {
			state[j] ^= data[i*16+j]
		}
		b.Encrypt(state, state)
	}
	for j := range state {
		state[j] ^= last[j] ^ k[j]
	}
	b.Encrypt(state, state)
	return state, nil
}

// Fixed OS3 CCM profile, NIST SP 800-38C: nonce 13, tag 4, L=2, AAD 00.
// It deliberately supports only this protocol profile, not a general AEAD API.
func ccmTag(b cipher.Block, nonce, plain []byte) []byte {
	state := make([]byte, 16)
	state[0] = 0x49
	copy(state[1:], nonce)
	binary.BigEndian.PutUint16(state[14:], uint16(len(plain)))
	b.Encrypt(state, state)
	// Encoded AAD: two-byte length 1, byte 00, then zero padding.
	state[1] ^= 1
	b.Encrypt(state, state)
	for off := 0; off < len(plain); off += 16 {
		n := len(plain) - off
		if n > 16 {
			n = 16
		}
		for i := 0; i < n; i++ {
			state[i] ^= plain[off+i]
		}
		b.Encrypt(state, state)
	}
	return state[:4]
}
func ccmStream(b cipher.Block, nonce []byte, count uint16) []byte {
	in := make([]byte, 16)
	in[0] = 1
	copy(in[1:], nonce)
	binary.BigEndian.PutUint16(in[14:], count)
	b.Encrypt(in, in)
	return in
}
func ccmSeal(b cipher.Block, nonce, plain []byte) ([]byte, error) {
	if len(nonce) != 13 || len(plain) > maxMessage-4 {
		return nil, errors.New("invalid CCM input")
	}
	out := make([]byte, len(plain)+4)
	tag := ccmTag(b, nonce, plain)
	s0 := ccmStream(b, nonce, 0)
	for off := 0; off < len(plain); off += 16 {
		s := ccmStream(b, nonce, uint16(off/16+1))
		for j := 0; j < 16 && off+j < len(plain); j++ {
			out[off+j] = plain[off+j] ^ s[j]
		}
	}
	for i := 0; i < 4; i++ {
		out[len(plain)+i] = tag[i] ^ s0[i]
	}
	return out, nil
}
func ccmOpen(b cipher.Block, nonce, in []byte) ([]byte, error) {
	if len(nonce) != 13 || len(in) < 4 || len(in) > maxMessage {
		return nil, errors.New("invalid CCM input")
	}
	n := len(in) - 4
	plain := make([]byte, n)
	for off := 0; off < n; off += 16 {
		s := ccmStream(b, nonce, uint16(off/16+1))
		for j := 0; j < 16 && off+j < n; j++ {
			plain[off+j] = in[off+j] ^ s[j]
		}
	}
	tag := ccmTag(b, nonce, plain)
	s0 := ccmStream(b, nonce, 0)
	for i := 0; i < 4; i++ {
		tag[i] ^= s0[i]
	}
	if subtle.ConstantTimeCompare(tag, in[n:]) != 1 {
		return nil, errors.New("Sesame authentication tag failed")
	}
	return plain, nil
}

type sessionCipher struct {
	block  cipher.Block
	token  [4]byte
	tx, rx uint64
}

func newCipher(secret, token []byte) (*sessionCipher, []byte, error) {
	if len(secret) != 16 || len(token) != 4 {
		return nil, nil, errors.New("unsupported Sesame key/token length")
	}
	auth, e := cmac(secret, token)
	if e != nil {
		return nil, nil, e
	}
	b, e := aes.NewCipher(auth)
	if e != nil {
		return nil, nil, e
	}
	s := &sessionCipher{block: b}
	copy(s.token[:], token)
	return s, auth, nil
}
func (s *sessionCipher) nonce(counter uint64) []byte {
	n := make([]byte, 13)
	binary.LittleEndian.PutUint64(n, counter)
	copy(n[9:], s.token[:])
	return n
}
func (s *sessionCipher) encrypt(b []byte) ([]byte, error) {
	if s.tx == math.MaxUint64 {
		return nil, errors.New("Sesame counter exhausted")
	}
	out, e := ccmSeal(s.block, s.nonce(s.tx), b)
	if e == nil {
		s.tx++
	}
	return out, e
}
func (s *sessionCipher) decrypt(b []byte) ([]byte, error) {
	if s.rx == math.MaxUint64 {
		return nil, errors.New("Sesame counter exhausted")
	}
	out, e := ccmOpen(s.block, s.nonce(s.rx), b)
	if e == nil {
		s.rx++
	}
	return out, e
}
