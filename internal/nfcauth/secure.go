// SPDX-License-Identifier: Apache-2.0
package nfcauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"fmt"
)

func xor(a, b []byte) []byte {
	r := make([]byte, len(a))
	for i := range a {
		r[i] = a[i] ^ b[i]
	}
	return r
}
func double(b []byte) []byte {
	r := make([]byte, 16)
	carry := byte(0)
	for i := 15; i >= 0; i-- {
		r[i] = (b[i] << 1) | carry
		carry = b[i] >> 7
	}
	if carry != 0 {
		r[15] ^= 0x87
	}
	return r
}

// RFC 4493 AES-CMAC.
func cmac(key, data []byte) ([]byte, error) {
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	l := make([]byte, 16)
	block.Encrypt(l, l)
	k1 := double(l)
	k2 := double(k1)
	n := (len(data) + 15) / 16
	if n == 0 {
		n = 1
	}
	last := make([]byte, 16)
	if len(data) > 0 && len(data)%16 == 0 {
		copy(last, data[(n-1)*16:])
		last = xor(last, k1)
	} else {
		remaining := data[(n-1)*16:]
		copy(last, remaining)
		last[len(remaining)] = 0x80
		last = xor(last, k2)
	}
	state := make([]byte, 16)
	for i := 0; i < n-1; i++ {
		block.Encrypt(state, xor(state, data[i*16:i*16+16]))
	}
	block.Encrypt(state, xor(state, last))
	return state, nil
}
func decryptResponse(data, kenc, krmac []byte, counter byte) ([]byte, error) {
	if len(data) < 24 || (len(data)-8)%16 != 0 {
		return nil, fmt.Errorf("invalid secure response length")
	}
	encrypted, mac := data[:len(data)-8], data[len(data)-8:]
	full, e := cmac(krmac, append(make([]byte, 16), encrypted...))
	if e != nil {
		return nil, e
	}
	if subtle.ConstantTimeCompare(mac, full[:8]) != 1 {
		return nil, fmt.Errorf("response MAC verification failed")
	}
	block, e := aes.NewCipher(kenc)
	if e != nil {
		return nil, e
	}
	pcb := make([]byte, 16)
	pcb[0] = 0x80
	pcb[15] = counter
	iv := make([]byte, 16)
	block.Encrypt(iv, pcb)
	plain := make([]byte, len(encrypted))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, encrypted)
	i := len(plain) - 1
	for i >= 0 && plain[i] == 0 {
		i--
	}
	if i < 0 || plain[i] != 0x80 || len(plain)-i > 16 {
		return nil, fmt.Errorf("invalid secure response padding")
	}
	return plain[:i], nil
}
