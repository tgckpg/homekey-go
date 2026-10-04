package blegateway

import "testing"

func TestReply(t *testing.T) {
	b := request(42)
	copy(b, "PONG")
	if !validReply(b, 42) {
		t.Fatal("valid reply rejected")
	}
	if validReply(b, 41) || validReply(b[:7], 42) || validReply(request(42), 42) {
		t.Fatal("stale or malformed reply accepted")
	}
}
func TestAddress(t *testing.T) {
	for _, a := range []string{"", "aa:bb:cc", "aabb.ccdd.eeff", "garbage"} {
		if ValidateAddress(a) == nil {
			t.Fatalf("accepted %q", a)
		}
	}
	if err := ValidateAddress("AA:BB:CC:DD:EE:FF"); err != nil {
		t.Fatal(err)
	}
}
