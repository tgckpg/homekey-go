package blegateway

import (
	"encoding/binary"
	"testing"
)

func TestRelayResponses(t *testing.T) {
	sid := uint32(0x12345678)
	seq := uint16(8)
	good := packet(0x11, sid, seq, 0, 2, []byte{0x90, 0})
	data, ready, e := parseResponse(good, sid, seq)
	if e != nil || !ready || len(data) != 2 {
		t.Fatal("valid response rejected")
	}
	for _, b := range [][]byte{packet(0x11, sid-1, seq, 0, 2, []byte{0x90, 0}), packet(0x11, sid, seq-1, 0, 2, []byte{0x90, 0}), packet(0x10, sid, seq, 0, 0, nil)} {
		_, ready, e := parseResponse(b, sid, seq)
		if e != nil || ready {
			t.Fatal("stale or pending response accepted")
		}
	}
	for _, b := range [][]byte{good[:13], packet(0x11, sid, seq, 1, 2, []byte{0x90, 0}), packet(0x12, sid, seq, 0, 1, []byte{3}), packet(0x11, sid, seq, 0, 1, []byte{0x90})} {
		if _, _, e := parseResponse(b, sid, seq); e == nil {
			t.Fatal("malformed/error response accepted")
		}
	}
}
func TestCardSession(t *testing.T) {
	b := make([]byte, 18)
	b[0] = 1
	b[1] = 1
	b[2] = 4
	b[3] = 0x20
	binary.BigEndian.PutUint32(b[4:], 123)
	copy(b[8:], []byte{1, 2, 3, 4})
	c, e := parseCard(b)
	if e != nil || c.Session != 123 || len(c.UID) != 4 || c.SAK != 0x20 {
		t.Fatal("card status rejected")
	}
	b[2] = 11
	if _, e := parseCard(b); e == nil {
		t.Fatal("bad UID accepted")
	}
}
