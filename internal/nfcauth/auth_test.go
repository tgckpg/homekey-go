package nfcauth

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"testing"

	"homekey.local/provisioner/internal/provision"
)

func hx(s string) []byte { b, _ := hex.DecodeString(s); return b }
func TestIndependentVectors(t *testing.T) {
	b, e := os.ReadFile("testdata/standard.json")
	if e != nil {
		t.Fatal(e)
	}
	var f map[string]string
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	for _, v := range []struct {
		name, key string
		n         int
	}{{"Volatile", "volatile", 48}, {"Persistent", "persistent", 32}} {
		got, e := derive(hx(f["shared"]), hx(f["nonce"]), hx(f["reader_pub"])[1:33], hx(f["device_pub"])[1:33], hx(f["versions"]), v.name, v.n)
		if e != nil || !bytes.Equal(got, hx(f[v.key])) {
			t.Fatalf("%s vector mismatch: %v", v.name, e)
		}
	}
	keys := hx(f["volatile"])
	got, e := decryptResponse(hx(f["response"]), keys[:16], keys[32:], 0)
	if e != nil || !bytes.Equal(got, hx(f["plaintext"])) {
		t.Fatalf("secure response vector: %v", e)
	}
	for i := range hx(f["response"]) {
		bad := hx(f["response"])
		bad[i] ^= 1
		if _, e := decryptResponse(bad, keys[:16], keys[32:], 0); e == nil {
			t.Fatalf("tampered byte %d accepted", i)
		}
	}
}
func TestCMACRFC4493(t *testing.T) {
	key := hx("2b7e151628aed2a6abf7158809cf4f3c")
	for _, v := range []struct{ data, mac string }{
		{"", "bb1d6929e95937287fa37d129b756746"},
		{"6bc1bee22e409f96e93d7e117393172a", "070a16b46b4d4144f79bdd9dd04a287c"},
		{"6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e5130c81c46a35ce411", "dfa66747de9ae63030ca32611497c827"},
	} {
		got, e := cmac(key, hx(v.data))
		if e != nil || !bytes.Equal(got, hx(v.mac)) {
			t.Fatalf("CMAC vector failed: %v", e)
		}
	}
}

// A phone simulator exercises reader proof, response MAC and endpoint proof.
// The independent vectors above separately verify KDF and encryption framing.
type phone struct {
	t     *testing.T
	creds provision.Credentials
	key   *ecdsa.PrivateKey
	eph   *ecdh.PrivateKey
	auth0 map[byte][]byte
	step  int
	mode  string
}

func (p *phone) Exchange(ctx context.Context, cmd []byte) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	p.step++
	ok := func(b []byte) []byte { return append(b, 0x90, 0) }
	switch p.step {
	case 1:
		if !bytes.Equal(cmd, []byte{0, 0xa4, 4, 0, 7, 0xa0, 0, 0, 8, 0x58, 1, 1, 0}) {
			p.t.Fatal("wrong SELECT")
		}
		if p.mode == "status" {
			return []byte{0x6a, 0x82}, nil
		}
		return ok(tlv(0x5c, []byte{2, 0, 1, 0})), nil
	case 2:
		if len(cmd) < 5 || !bytes.Equal(cmd[:4], []byte{0x80, 0x80, 0, 1}) || int(cmd[4]) != len(cmd)-5 {
			p.t.Fatal("wrong AUTH0 framing")
		}
		f, e := fields(cmd[5:])
		if e != nil {
			return nil, e
		}
		p.auth0 = f
		scalar := hx(p.creds.ReaderPrivateKey)
		h := sha256.Sum256(append([]byte("key-identifier"), scalar...))
		id := append(h[:8], hx(p.creds.ReaderIdentifier)...)
		if !bytes.Equal(f[0x4d], id) || len(f[0x4c]) != 16 {
			p.t.Fatal("wrong reader ID or nonce")
		}
		if p.mode == "bad-point" {
			return ok(tlv(0x86, make([]byte, 65))), nil
		}
		p.eph, e = ecdh.P256().GenerateKey(rand.Reader)
		if e != nil {
			return nil, e
		}
		return ok(tlv(0x86, p.eph.PublicKey().Bytes())), nil
	case 3:
		if !bytes.Equal(cmd[:4], []byte{0x80, 0x81, 0, 0}) {
			p.t.Fatal("wrong AUTH1 framing")
		}
		f, e := fields(cmd[5:])
		if e != nil {
			return nil, e
		}
		a := p.auth0
		dx := p.eph.PublicKey().Bytes()[1:33]
		rx := a[0x87][1:33]
		scalar := hx(p.creds.ReaderPrivateKey)
		x, y := elliptic.P256().ScalarBaseMult(scalar)
		digest := sha256.Sum256(transcript(a[0x4d], dx, rx, a[0x4c], []byte{0x41, 0x5d, 0x95, 0x69}))
		sig := f[0x9e]
		if len(sig) != 64 || !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			p.t.Fatal("reader proof invalid")
		}
		devicePub := elliptic.Marshal(elliptic.P256(), p.key.X, p.key.Y)
		eid := sha1.Sum(devicePub)
		digest = sha256.Sum256(transcript(a[0x4d], dx, rx, a[0x4c], []byte{0x4e, 0x88, 0x7b, 0x4c}))
		r, s, e := ecdsa.Sign(rand.Reader, p.key, digest[:])
		if e != nil {
			return nil, e
		}
		proof := make([]byte, 64)
		r.FillBytes(proof[:32])
		s.FillBytes(proof[32:])
		if p.mode == "bad-signature" {
			proof[0] ^= 1
		}
		plain := append(tlv(0x4e, eid[:6]), tlv(0x9e, proof)...)
		readerEph, e := ecdh.P256().NewPublicKey(a[0x87])
		if e != nil {
			return nil, e
		}
		shared, e := p.eph.ECDH(readerEph)
		if e != nil {
			return nil, e
		}
		keys, e := derive(shared, a[0x4c], rx, dx, []byte{2, 0, 1, 0}, "Volatile", 48)
		if e != nil {
			return nil, e
		}
		block, _ := aes.NewCipher(keys[:16])
		iv := make([]byte, 16)
		pcb := make([]byte, 16)
		pcb[0] = 0x80
		block.Encrypt(iv, pcb)
		plain = append(plain, 0x80)
		for len(plain)%16 != 0 {
			plain = append(plain, 0)
		}
		encrypted := make([]byte, len(plain))
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, plain)
		mac, _ := cmac(keys[32:], append(make([]byte, 16), encrypted...))
		out := append(encrypted, mac[:8]...)
		if p.mode == "bad-mac" {
			out[len(out)-1] ^= 1
		}
		return ok(out), nil
	}
	return nil, fmt.Errorf("unexpected command")
}
func TestAuthenticate(t *testing.T) {
	for _, mode := range []string{"valid", "inactive", "unknown", "bad-point", "bad-signature", "bad-mac", "status"} {
		t.Run(mode, func(t *testing.T) {
			reader, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			device, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			scalar := make([]byte, 32)
			reader.D.FillBytes(scalar)
			raw := elliptic.Marshal(elliptic.P256(), device.X, device.Y)
			id := sha1.Sum(raw)
			eid := hex.EncodeToString(id[:6])
			ep := provision.Endpoint{PublicKey: hex.EncodeToString(raw), KeyType: 2, KeyState: 1}
			if mode == "inactive" {
				ep.KeyState = 0
			}
			endpoints := map[string]provision.Endpoint{eid: ep}
			if mode == "unknown" {
				endpoints = map[string]provision.Endpoint{}
			}
			c := provision.Credentials{ReaderPrivateKey: hex.EncodeToString(scalar), ReaderIdentifier: "0102030405060708", Issuers: map[string]provision.Issuer{"issuer": {Endpoints: endpoints}}}
			p := &phone{t: t, creds: c, key: device, mode: mode}
			result, e := Authenticate(context.Background(), p, c)
			if mode == "valid" {
				if e != nil || result == nil || result.EndpointID != eid || len(result.PersistentKey) != 32 {
					t.Fatalf("valid auth failed: %v", e)
				}
			} else if e == nil || result != nil {
				t.Fatalf("%s accepted", mode)
			}
		})
	}
}
func TestBERRejectsMalformed(t *testing.T) {
	for _, b := range [][]byte{{0x5c}, {0x5c, 4, 2, 0}, {0x5c, 0x80}, {0x5c, 0x81, 2, 2, 0}, {0x5c, 2, 2, 0, 0x5c, 2, 1, 0}} {
		if _, e := fields(b); e == nil {
			t.Fatalf("accepted malformed BER %x", b)
		}
	}
}
