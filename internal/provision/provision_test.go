package provision

import (
	"bytes"
	"crypto/elliptic"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func mustHex(s string) []byte {
	b, e := hex.DecodeString(s)
	if e != nil {
		panic(e)
	}
	return b
}
func request(op, tag byte, inner []byte) []byte {
	return append(TLV(1, []byte{op}), TLV(tag, inner)...)
}
func readerAdd() []byte {
	key := make([]byte, 32)
	key[31] = 1
	b := append(TLV(1, []byte{2}), TLV(2, key)...)
	b = append(b, TLV(3, []byte("reader01"))...)
	return request(2, 6, b)
}
func paired(t *testing.T) (*Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	pub := bytes.Repeat([]byte{0x42}, 32)
	p, _ := json.Marshal(struct {
		Name       string
		PublicKey  []byte
		Permission byte
	}{"controller", pub, 1})
	if e = s.Set("controller.pairing", p); e != nil {
		t.Fatal(e)
	}
	return s, dir, keyID(pub)
}
func endpointAdd(iid string) []byte {
	x, y := elliptic.P256().ScalarBaseMult([]byte{2})
	pub := elliptic.Marshal(elliptic.P256(), x, y)
	b := append(TLV(1, []byte{2}), TLV(2, pub[1:])...)
	b = append(b, TLV(3, mustHex(iid))...)
	b = append(b, TLV(4, []byte{1})...)
	return request(2, 4, b)
}
func checkResponse(t *testing.T, s *Store, req []byte, want []byte) {
	t.Helper()
	got, e := s.Handle(req)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}
func TestProvisionAndRestart(t *testing.T) {
	s, dir, iid := paired(t)
	checkResponse(t, s, readerAdd(), mustHex("0703020100"))
	checkResponse(t, s, readerAdd(), mustHex("0703020102"))
	key := make([]byte, 32)
	key[31] = 1
	checkResponse(t, s, request(1, 6, nil), TLV(7, TLV(1, mustHex(keyID(key)))))
	want := TLV(5, append(TLV(2, mustHex(iid)), TLV(3, []byte{Success})...))
	checkResponse(t, s, endpointAdd(iid), want)
	want = TLV(5, append(TLV(2, mustHex(iid)), TLV(3, []byte{Duplicate})...))
	checkResponse(t, s, endpointAdd(iid), want)
	s2, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if v := s2.Summary(); !v.ReaderProvisioned || v.Endpoints != 1 || v.Issuers != 1 {
		t.Fatalf("bad summary %+v", v)
	}
	info, e := os.Stat(filepath.Join(dir, "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %o", info.Mode().Perm())
	}
	if e = s2.Delete("controller.pairing"); e != nil {
		t.Fatal(e)
	}
	if v := s2.Summary(); v.ReaderProvisioned || v.Endpoints != 0 || v.Issuers != 0 {
		t.Fatalf("unpair did not clear credentials %+v", v)
	}
	s3, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if s3.Summary().ReaderProvisioned {
		t.Fatal("revocation not persisted")
	}
}
func TestReaderRemove(t *testing.T) {
	s, _, _ := paired(t)
	checkResponse(t, s, readerAdd(), mustHex("0703020100"))
	key := make([]byte, 32)
	key[31] = 1
	checkResponse(t, s, request(3, 6, TLV(4, mustHex(keyID(key)))), mustHex("0703020100"))
	checkResponse(t, s, request(3, 6, TLV(4, mustHex(keyID(key)))), mustHex("0703020103"))
}
func TestUnknownIssuerAndInvalidKey(t *testing.T) {
	s, _, _ := paired(t)
	checkResponse(t, s, readerAdd(), mustHex("0703020100"))
	checkResponse(t, s, endpointAdd("0000000000000000"), mustHex("0503030103"))
	f, _ := DecodeTLV(readerAdd())
	inner, _ := DecodeTLV(f[6])
	inner[2] = make([]byte, 32)
	b := append(TLV(1, inner[1]), TLV(2, inner[2])...)
	b = append(b, TLV(3, inner[3])...)
	if _, e := s.Handle(request(2, 6, b)); e == nil {
		t.Fatal("zero scalar accepted")
	}
	if !s.Summary().ReaderProvisioned {
		t.Fatal("valid reader key lost")
	}
}
func TestRejectMalformedTLV(t *testing.T) {
	for _, b := range [][]byte{{1}, {1, 2, 0}, {1, 1, 1, 1, 1, 2}, {255, 0}, make([]byte, MaxMessage+1)} {
		if _, e := DecodeTLV(b); e == nil {
			t.Fatalf("accepted malformed input %x", b[:min(8, len(b))])
		}
	}
	for _, n := range []int{0, 1, 254, 255, 256, 510, 511} {
		b := bytes.Repeat([]byte{42}, n)
		f, e := DecodeTLV(TLV(6, b))
		if e != nil {
			t.Fatal(e)
		}
		v, ok := f[6]
		if !ok || !bytes.Equal(v, b) {
			t.Fatal("fragment roundtrip failed")
		}
	}
}
func TestDiskFailureRollsBack(t *testing.T) {
	s, _, _ := paired(t)
	s.path = filepath.Join(t.TempDir(), "missing", "state.json")
	if _, e := s.Handle(readerAdd()); e == nil {
		t.Fatal("disk failure ignored")
	}
	if s.Summary().ReaderProvisioned {
		t.Fatal("memory committed on failed disk write")
	}
}
func TestCorruptStateFailsClosed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if e := os.WriteFile(p, []byte("{bad"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(dir); e == nil {
		t.Fatal("corrupt state silently reset")
	}
}
func TestConcurrentRequests(t *testing.T) {
	s, _, iid := paired(t)
	checkResponse(t, s, readerAdd(), mustHex("0703020100"))
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if _, e := s.Handle(endpointAdd(iid)); e != nil {
					t.Error(e)
				}
				_ = s.Summary()
			}
		}()
	}
	wg.Wait()
	if s.Summary().Endpoints != 1 {
		t.Fatal("duplicate endpoints")
	}
}
func FuzzDecodeTLV(f *testing.F) {
	f.Add([]byte{1, 1, 2, 6, 0})
	f.Add(bytes.Repeat([]byte{1}, 256))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = DecodeTLV(b) })
}
