package sesame

import (
	"bytes"
	"crypto/aes"
	"encoding/hex"
	"github.com/godbus/dbus/v5"
	"testing"
)

func unhex(s string) []byte {
	b, e := hex.DecodeString(s)
	if e != nil {
		panic(e)
	}
	return b
}
func TestCMACRFC4493(t *testing.T) {
	key := unhex("2b7e151628aed2a6abf7158809cf4f3c")
	for _, v := range [][2]string{{"", "bb1d6929e95937287fa37d129b756746"}, {"6bc1bee22e409f96e93d7e117393172a", "070a16b46b4d4144f79bdd9dd04a287c"}, {"6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e5130c81c46a35ce411", "dfa66747de9ae63030ca32611497c827"}} {
		got, e := cmac(key, unhex(v[0]))
		if e != nil || hex.EncodeToString(got) != v[1] {
			t.Fatalf("CMAC mismatch %x %v", got, e)
		}
	}
}
func TestCCMIndependentVectors(t *testing.T) {
	// Generated independently with Python cryptography/OpenSSL AESCCM, 4-byte
	// tag and AAD 00. Nonzero upper counter bytes catch the SDK's stale comment.
	key := unhex("000102030405060708090a0b0c0d0e0f")
	token := unhex("11223344")
	c, auth, e := newCipher(key, token)
	if e != nil || hex.EncodeToString(auth) != "1d7c7f1d52493a30cc2ee841f461afaf" {
		t.Fatal("session CMAC", e)
	}
	vectors := map[int]string{0: "e9edea60", 1: "6d19848f18", 16: "6d872d6cad7752dde946535210729276a0c5f8de", 17: "6d872d6cad7752dde946535210729276b241e495c5", 40: "6d872d6cad7752dde946535210729276b20637e7243ebf8f473862b5b847d68743047366a43bfe0b98f3ce1f"}
	for n, want := range vectors {
		p := make([]byte, n)
		for i := range p {
			p[i] = byte(i)
		}
		c.tx = 0x0102030405060708
		c.rx = c.tx
		got, e := c.encrypt(p)
		if e != nil || hex.EncodeToString(got) != want {
			t.Fatalf("n=%d: %x %v", n, got, e)
		}
		plain, e := c.decrypt(got)
		if e != nil || !bytes.Equal(p, plain) {
			t.Fatalf("decrypt: %v", e)
		}
		c.rx--
		got[len(got)-1] ^= 1
		before := c.rx
		if _, e = c.decrypt(got); e == nil || c.rx != before {
			t.Fatal("accepted corrupt tag or advanced counter")
		}
	}
	if _, _, e := newCipher(key, token[:3]); e == nil {
		t.Fatal("accepted bad token")
	}
	b, _ := aes.NewCipher(key)
	if _, e := ccmOpen(b, make([]byte, 13), []byte{1}); e == nil {
		t.Fatal("accepted short CCM")
	}
}
func TestFragmentBoundaries(t *testing.T) {
	for _, n := range []int{1, 19, 20, 38, 39, 1024} {
		p := make([]byte, n)
		for i := range p {
			p[i] = byte(i)
		}
		for _, kind := range []byte{1, 2} {
			fs, e := fragments(kind, p)
			if e != nil {
				t.Fatal(e)
			}
			var a assembler
			var out []byte
			for i, f := range fs {
				k, b, e := a.feed(f)
				if e != nil {
					t.Fatal(e)
				}
				if i < len(fs)-1 && k != 0 {
					t.Fatal("early completion")
				}
				if i == len(fs)-1 {
					if k != kind {
						t.Fatal("wrong kind")
					}
					out = b
				}
			}
			if !bytes.Equal(p, out) {
				t.Fatal("reassembly")
			}
		}
	}
	for _, p := range [][]byte{nil, {1}, {6, 1}, {0, 1}} {
		var a assembler
		if _, _, e := a.feed(p); e == nil {
			t.Fatalf("accepted malformed %x", p)
		}
	}
	var a assembler
	a.feed([]byte{1, 0})
	if _, _, e := a.feed([]byte{3, 0}); e == nil {
		t.Fatal("accepted interrupted message")
	}
	a = assembler{}
	a.feed(append([]byte{1}, make([]byte, 19)...))
	for i := 0; i < 52; i++ {
		a.feed(append([]byte{0}, make([]byte, 19)...))
	}
	if _, _, e := a.feed(append([]byte{0}, make([]byte, 19)...)); e == nil {
		t.Fatal("unbounded message")
	}
}
func TestMechanicalStatusAndBoundary(t *testing.T) {
	var s State
	if e := s.publish(81, []byte{0, 0, 0, 128, 255, 255, 0x3a}); e != nil {
		t.Fatal(e)
	}
	if *s.Position != -1 || s.Target != nil || !s.Locked || !s.Stopped || !s.Critical || !s.BatteryCritical {
		t.Fatalf("bad status %+v", s)
	}
	if e := s.publish(20, []byte{255, 255}); e != nil || s.Boundary == nil || *s.Boundary != -1 {
		t.Fatal("-1 is a valid boundary")
	}
	if e := s.publish(80, []byte{90, 0, 0, 0, 0, 0}); e != nil || *s.LockPosition != 90 || *s.UnlockPosition != 0 {
		t.Fatal("settings")
	}
	if e := s.publish(81, []byte{1}); e == nil {
		t.Fatal("short status")
	}
}
func TestAdvertisement(t *testing.T) {
	b := append([]byte{21, 0, 1}, unhex("00112233445566778899aabbccddeeff")...)
	d, ok := Advertisement(b)
	if !ok || d.UUID != "00112233-4455-6677-8899-aabbccddeeff" || d.Model != "sesame_6_pro" || !d.Registered {
		t.Fatal(d)
	}
	b[0] = 1
	if _, ok := Advertisement(b); ok {
		t.Fatal("accepted OS2")
	}
	if _, ok := Advertisement([]byte{21}); ok {
		t.Fatal("short advertisement")
	}
}

func TestDiscoveryPrefersFreshAddressForSameUUID(t *testing.T) {
	oldPath := dbus.ObjectPath("/org/bluez/hci0/dev_00_00_00_00_00_01")
	newPath := dbus.ObjectPath("/org/bluez/hci0/dev_FF_FF_FF_FF_FF_FF")
	advertisement := append([]byte{21, 0, 1}, unhex("00112233445566778899aabbccddeeff")...)
	makeProps := func(address string) map[string]dbus.Variant {
		return map[string]dbus.Variant{
			"ManufacturerData": dbus.MakeVariant(map[uint16]dbus.Variant{0x055a: dbus.MakeVariant(advertisement)}),
			"Address":          dbus.MakeVariant(address), "Adapter": dbus.MakeVariant(dbus.ObjectPath("/org/bluez/hci0")),
		}
	}
	o := objects{oldPath: {"org.bluez.Device1": makeProps("00:00:00:00:00:01")}, newPath: {"org.bluez.Device1": makeProps("FF:FF:FF:FF:FF:FF")}}
	for i := 0; i < 20; i++ {
		path, target := chooseTarget(o, testTarget, map[dbus.ObjectPath]uint64{newPath: 1})
		if path != newPath || target.Address != "FF:FF:FF:FF:FF:FF" {
			t.Fatal("selected stale reboot address", path, target)
		}
	}
	path, _ := chooseTarget(o, testTarget, map[dbus.ObjectPath]uint64{newPath: 1, oldPath: 2})
	if path != oldPath {
		t.Fatal("did not use newest advertisement")
	}
	wrong := testTarget
	wrong.Adapter = "hci1"
	if path, _ := chooseTarget(o, wrong, nil); path != "" {
		t.Fatal("crossed adapter identities")
	}
}
