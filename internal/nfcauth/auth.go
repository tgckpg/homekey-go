// SPDX-License-Identifier: Apache-2.0
// Home Key NFC authentication, based on kormax/apple-home-key-reader;
// see NOTICE. This implementation uses the STANDARD flow for enrolled devices.
package nfcauth

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"math/big"

	"golang.org/x/crypto/hkdf"
	"homekey.local/provisioner/internal/provision"
)

type Transport interface {
	Exchange(context.Context, []byte) ([]byte, error)
}
type Result struct {
	IssuerID, EndpointID, PublicKey string
	PersistentKey                   []byte
}

func tlv(tag byte, value []byte) []byte {
	b := []byte{tag}
	if len(value) < 128 {
		b = append(b, byte(len(value)))
	} else {
		b = append(b, 0x81, byte(len(value)))
	}
	return append(b, value...)
}

// Authentication messages use BER-TLV, not HAP's TLV8.
func fields(data []byte) (map[byte][]byte, error) {
	f := map[byte][]byte{}
	for len(data) > 0 {
		if len(data) < 2 {
			return nil, fmt.Errorf("truncated BER-TLV")
		}
		tag, n := data[0], int(data[1])
		data = data[2:]
		if tag&0x1f == 0x1f {
			return nil, fmt.Errorf("unsupported BER-TLV tag")
		}
		if n == 0x81 {
			if len(data) < 1 {
				return nil, fmt.Errorf("truncated BER length")
			}
			n = int(data[0])
			data = data[1:]
			if n < 128 {
				return nil, fmt.Errorf("noncanonical BER length")
			}
		} else if n >= 128 {
			return nil, fmt.Errorf("unsupported BER length")
		}
		if n > len(data) {
			return nil, fmt.Errorf("truncated BER value")
		}
		if _, ok := f[tag]; ok {
			return nil, fmt.Errorf("duplicate BER tag %02x", tag)
		}
		f[tag] = append([]byte(nil), data[:n]...)
		data = data[n:]
	}
	return f, nil
}
func checked(ctx context.Context, t Transport, name string, cmd []byte) ([]byte, error) {
	r, e := t.Exchange(ctx, cmd)
	if e != nil {
		return nil, fmt.Errorf("%s: %w", name, e)
	}
	if len(r) < 2 {
		return nil, fmt.Errorf("%s: missing status word", name)
	}
	if !bytes.Equal(r[len(r)-2:], []byte{0x90, 0}) {
		return nil, fmt.Errorf("%s: status %02x%02x", name, r[len(r)-2], r[len(r)-1])
	}
	return r[:len(r)-2], nil
}
func command(ins, p1, p2 byte, data []byte) []byte {
	return append([]byte{0x80, ins, p1, p2, byte(len(data))}, data...)
}
func transcript(id, deviceX, readerX, nonce, constant []byte) []byte {
	b := tlv(0x4d, id)
	b = append(b, tlv(0x86, deviceX)...)
	b = append(b, tlv(0x87, readerX)...)
	b = append(b, tlv(0x4c, nonce)...)
	return append(b, tlv(0x93, constant)...)
}
func derive(shared, nonce, readerX, deviceX, versions []byte, contextName string, n int) ([]byte, error) {
	// ANSI X9.63 SHA-256, 32 bytes: SHA256(Z || counter BE32 || nonce).
	h := sha256.New()
	h.Write(shared)
	h.Write([]byte{0, 0, 0, 1})
	h.Write(nonce)
	key := h.Sum(nil)
	info := append([]byte(nil), readerX...)
	info = append(info, deviceX...)
	info = append(info, nonce...)
	info = append(info, 0x5e, 0, 1)
	info = append(info, []byte(contextName)...)
	info = append(info, tlv(0x5c, []byte{2, 0})...)
	info = append(info, tlv(0x5c, versions)...)
	out := make([]byte, n)
	_, e := io.ReadFull(hkdf.New(sha256.New, key, nil, info), out)
	return out, e
}

// Authenticate proves possession of an ACTIVE, already-enrolled device key.
// A successful SELECT, ECDH or 9000 status alone never grants access.
func Authenticate(ctx context.Context, t Transport, c provision.Credentials) (*Result, error) {
	scalar, e := hex.DecodeString(c.ReaderPrivateKey)
	if e != nil || len(scalar) != 32 {
		return nil, fmt.Errorf("reader is not provisioned")
	}
	unique, e := hex.DecodeString(c.ReaderIdentifier)
	if e != nil || len(unique) != 8 {
		return nil, fmt.Errorf("invalid reader identifier")
	}
	d := new(big.Int).SetBytes(scalar)
	curve := elliptic.P256()
	if d.Sign() == 0 || d.Cmp(curve.Params().N) >= 0 {
		return nil, fmt.Errorf("invalid reader scalar")
	}
	x, y := curve.ScalarBaseMult(scalar)
	reader := &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y}, D: d}
	groupHash := sha256.Sum256(append([]byte("key-identifier"), scalar...))
	id := append(append([]byte(nil), groupHash[:8]...), unique...)
	selected, e := checked(ctx, t, "SELECT", []byte{0, 0xa4, 4, 0, 7, 0xa0, 0, 0, 8, 0x58, 1, 1, 0})
	if e != nil {
		return nil, e
	}
	sf, e := fields(selected)
	if e != nil {
		return nil, e
	}
	versions := sf[0x5c]
	if len(versions) == 0 || len(versions)%2 != 0 {
		return nil, fmt.Errorf("invalid supported versions")
	}
	supported := false
	for i := 0; i < len(versions); i += 2 {
		if bytes.Equal(versions[i:i+2], []byte{2, 0}) {
			supported = true
		}
	}
	if !supported {
		return nil, fmt.Errorf("Home Key protocol 2.0 not supported")
	}
	log.Print("Home Key SELECT accepted; protocol=2.0")
	eph, e := ecdh.P256().GenerateKey(rand.Reader)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	pub := eph.PublicKey().Bytes()
	data := tlv(0x5c, []byte{2, 0})
	data = append(data, tlv(0x87, pub)...)
	data = append(data, tlv(0x4c, nonce)...)
	data = append(data, tlv(0x4d, id)...)
	// STANDARD flag 0, UNLOCK transaction code 1.
	auth0, e := checked(ctx, t, "AUTH0", command(0x80, 0, 1, data))
	if e != nil {
		return nil, e
	}
	af, e := fields(auth0)
	if e != nil {
		return nil, e
	}
	devicePub, e := ecdh.P256().NewPublicKey(af[0x86])
	if e != nil {
		return nil, fmt.Errorf("AUTH0: invalid device ephemeral key")
	}
	log.Print("Home Key AUTH0 key exchange complete")
	shared, e := eph.ECDH(devicePub)
	if e != nil {
		return nil, e
	}
	deviceX := devicePub.Bytes()[1:33]
	readerX := pub[1:33]
	digest := sha256.Sum256(transcript(id, deviceX, readerX, nonce, []byte{0x41, 0x5d, 0x95, 0x69}))
	r, s, e := ecdsa.Sign(rand.Reader, reader, digest[:])
	if e != nil {
		return nil, e
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	encrypted, e := checked(ctx, t, "AUTH1", command(0x81, 0, 0, tlv(0x9e, signature)))
	if e != nil {
		return nil, e
	}
	keys, e := derive(shared, nonce, readerX, deviceX, versions, "Volatile", 48)
	if e != nil {
		return nil, e
	}
	plain, e := decryptResponse(encrypted, keys[:16], keys[32:], 0)
	if e != nil {
		return nil, fmt.Errorf("AUTH1: %w", e)
	}
	df, e := fields(plain)
	if e != nil {
		return nil, e
	}
	if len(df[0x4e]) != 6 || len(df[0x9e]) != 64 {
		return nil, fmt.Errorf("AUTH1: invalid endpoint proof")
	}
	eid := hex.EncodeToString(df[0x4e])
	proof := df[0x9e]
	digest = sha256.Sum256(transcript(id, deviceX, readerX, nonce, []byte{0x4e, 0x88, 0x7b, 0x4c}))
	for iid, issuer := range c.Issuers {
		ep, ok := issuer.Endpoints[eid]
		if !ok || ep.KeyState != 1 || ep.KeyType != 2 {
			continue
		}
		raw, e := hex.DecodeString(ep.PublicKey)
		if e != nil {
			continue
		}
		x, y := elliptic.Unmarshal(curve, raw)
		if x == nil {
			continue
		}
		if !ecdsa.Verify(&ecdsa.PublicKey{Curve: curve, X: x, Y: y}, digest[:], new(big.Int).SetBytes(proof[:32]), new(big.Int).SetBytes(proof[32:])) {
			continue
		}
		persistent, e := derive(shared, nonce, readerX, deviceX, versions, "Persistent", 32)
		if e != nil {
			return nil, e
		}
		return &Result{IssuerID: iid, EndpointID: eid, PublicKey: ep.PublicKey, PersistentKey: persistent}, nil
	}
	return nil, fmt.Errorf("AUTH1: unknown, inactive or invalid endpoint")
}

// Finish controls phone UX. The caller must recheck current authorization
// before sending success. Failure is also sent when authentication is rejected.
func Finish(ctx context.Context, t Transport, success bool) error {
	p1 := byte(0)
	if success {
		p1 = 1
	}
	_, e := checked(ctx, t, "CONTROL FLOW", []byte{0x80, 0x3c, p1, 0})
	return e
}
