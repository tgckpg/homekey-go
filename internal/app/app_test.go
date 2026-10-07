package app

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"encoding/hex"
	"encoding/json"
	"homekey.local/provisioner/internal/nfcauth"
	"homekey.local/provisioner/internal/provision"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testLock(t *testing.T) *Lock {
	t.Helper()
	s, err := provision.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewLock(LockConfig{ID: "front-door", Name: "Front Door", Port: 51826, Finish: "silver", Readers: []string{"porch"}}, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func setupRequest(l *Lock) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	l.Device.Server.ServeMux().(http.Handler).ServeHTTP(w, httptest.NewRequest("POST", "/pair-setup", bytes.NewReader([]byte{6, 1, 1, 0, 1, 0})))
	return w
}
func TestPairingWindow(t *testing.T) {
	l := testLock(t)
	if s := l.Status(); s.PIN != "" {
		t.Fatal("PIN visible before Pair")
	}
	if r := setupRequest(l); !bytes.Equal(r.Body.Bytes(), []byte{6, 1, 2, 7, 1, 6}) {
		t.Fatalf("closed window allowed setup: %x", r.Body.Bytes())
	}
	if err := l.Pair(); err != nil {
		t.Fatal(err)
	}
	if l.Status().PIN == "" {
		t.Fatal("Pair did not show code")
	}
	if r := setupRequest(l); bytes.Equal(r.Body.Bytes(), []byte{6, 1, 2, 7, 1, 6}) || r.Code != 200 {
		t.Fatalf("open setup failed: %d %x", r.Code, r.Body.Bytes())
	}
	l.CancelPair()
	if l.Status().PIN != "" {
		t.Fatal("cancel retained code")
	}
	l.Pair()
	l.until = time.Now().Add(-time.Second)
	if l.Status().PIN != "" {
		t.Fatal("expired code visible")
	}
	if r := setupRequest(l); !bytes.Equal(r.Body.Bytes(), []byte{6, 1, 2, 7, 1, 6}) {
		t.Fatal("expired pairing allowed")
	}
	l.Pair()
	raw, _ := json.Marshal(struct {
		Name       string
		PublicKey  []byte
		Permission byte
	}{"phone", bytes.Repeat([]byte{42}, 32), 1})
	if err := l.Store.Set("phone.pairing", raw); err != nil {
		t.Fatal(err)
	}
	if l.Status().PIN != "" {
		t.Fatal("paired lock leaked setup code")
	}
	if err := l.Pair(); err == nil {
		t.Fatal("already paired lock reopened")
	}
	if r := setupRequest(l); !bytes.Equal(r.Body.Bytes(), []byte{6, 1, 2, 7, 1, 6}) {
		t.Fatal("paired lock allowed setup")
	}
}
func TestConfigurationValidation(t *testing.T) {
	c := Config{Locks: []LockConfig{{ID: "front", Name: "Front", Port: 51826, Finish: "silver", Readers: []string{"porch"}}, {ID: "back", Name: "Back", Port: 51827, Finish: "black", Readers: []string{"porch"}}}, Readers: []ReaderConfig{{ID: "porch", Address: "70:AF:09:16:42:5A", Adapter: "hci0"}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){func(c *Config) { c.Locks[0].ID = "../escape" }, func(c *Config) { c.Locks[1].ID = "front" }, func(c *Config) { c.Locks[1].Port = 51826 }, func(c *Config) { c.Locks[0].Readers = []string{"missing"} }, func(c *Config) { c.Readers[0].Adapter = "hci/0" }, func(c *Config) {
		c.Readers = append(c.Readers, ReaderConfig{ID: "other", Address: "70:af:09:16:42:5a", Adapter: "hci1"})
	}} {
		b, _ := json.Marshal(c)
		var next Config
		json.Unmarshal(b, &next)
		change(&next)
		if next.Validate() == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}
func TestIngressAndActions(t *testing.T) {
	l := testLock(t)
	handler := Handler([]*Lock{l}, nil, true)
	request := func(remote, path, header string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Homekey-Action", header)
		r.Header.Set("Origin", "https://ha.example.com")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request("10.0.0.1:1000", "/api/locks/front-door/pair", "pairing"); w.Code != 403 {
		t.Fatal("LAN bypassed ingress")
	}
	if w := request("172.30.32.2:1000", "/api/locks/front-door/pair", ""); w.Code != 403 {
		t.Fatal("missing action header accepted")
	}
	if w := request("172.30.32.2:1000", "/api/locks/front-door/pair", "pairing"); w.Code != 204 {
		t.Fatalf("HA origin rejected: %d %s", w.Code, w.Body.String())
	}
	if w := request("172.30.32.2:1000", "/api/locks/front-door/cancel", "pairing"); w.Code != 204 || l.Status().PIN != "" {
		t.Fatal("cancel failed")
	}
}
func TestReaderAssignmentsAndGroups(t *testing.T) {
	a, b := testLock(t), testLock(t)
	b.Config.ID = "back"
	b.Config.Readers = []string{"other"}
	if assigned := Assigned([]*Lock{a, b}, "porch"); len(assigned) != 1 || assigned[0] != a {
		t.Fatal("incorrect assignment")
	}
	b.Config.Readers = []string{"porch"}
	if len(Assigned([]*Lock{a, b}, "porch")) != 2 {
		t.Fatal("shared reader not supported")
	}
	// Use HomeKit pairing + normal provisioning control to install group keys.
	for i, l := range []*Lock{a, b} {
		raw, _ := json.Marshal(struct {
			Name       string
			PublicKey  []byte
			Permission byte
		}{"phone", bytes.Repeat([]byte{42}, 32), 1})
		if err := l.Store.Set("phone.pairing", raw); err != nil {
			t.Fatal(err)
		}
		key := make([]byte, 32)
		key[31] = byte(i + 1)
		body := append(provision.TLV(1, []byte{2}), provision.TLV(2, key)...)
		body = append(body, provision.TLV(3, []byte("reader01"))...)
		request := append(provision.TLV(1, []byte{2}), provision.TLV(6, body)...)
		if _, err := l.Store.Handle(request); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Group([]*Lock{a, b}); err == nil {
		t.Fatal("different Home groups accepted")
	}
	if group, err := Group([]*Lock{a}); err != nil || len(group) != 8 {
		t.Fatal("single group rejected")
	}
}

func TestSharedReaderPerLockAuthorization(t *testing.T) {
	a, b, c := testLock(t), testLock(t), testLock(t)
	b.Config.ID = "back"
	c.Config.ID = "unenrolled"
	snapshots := map[*Lock]provision.Credentials{}
	result := &nfcauth.Result{}
	for _, l := range []*Lock{a, b, c} {
		raw, _ := json.Marshal(struct {
			Name       string
			PublicKey  []byte
			Permission byte
		}{"phone", bytes.Repeat([]byte{42}, 32), 1})
		if err := l.Store.Set("phone.pairing", raw); err != nil {
			t.Fatal(err)
		}
		key := make([]byte, 32)
		key[31] = 1
		body := append(provision.TLV(1, []byte{2}), provision.TLV(2, key)...)
		body = append(body, provision.TLV(3, []byte("reader01"))...)
		if _, err := l.Store.Handle(append(provision.TLV(1, []byte{2}), provision.TLV(6, body)...)); err != nil {
			t.Fatal(err)
		}
		if l != c {
			var iid string
			for id := range l.Store.AuthenticationCredentials().Issuers {
				iid = id
			}
			issuerID, _ := hex.DecodeString(iid)
			x, y := elliptic.P256().ScalarBaseMult([]byte{2})
			pub := elliptic.Marshal(elliptic.P256(), x, y)
			body := append(provision.TLV(1, []byte{2}), provision.TLV(2, pub[1:])...)
			body = append(body, provision.TLV(3, issuerID)...)
			body = append(body, provision.TLV(4, []byte{1})...)
			if _, err := l.Store.Handle(append(provision.TLV(1, []byte{2}), provision.TLV(4, body)...)); err != nil {
				t.Fatal(err)
			}
			for eid, ep := range l.Store.AuthenticationCredentials().Issuers[iid].Endpoints {
				result.IssuerID = iid
				result.EndpointID = eid
				result.PublicKey = ep.PublicKey
			}
		}
		snapshots[l] = l.Store.AuthenticationCredentials()
	}
	groupKey := snapshots[a].ReaderPrivateKey
	if n := unlockAuthorized(context.Background(), []*Lock{a, b, c}, snapshots, groupKey, result, "porch", 1); n != 2 {
		t.Fatalf("unlocked %d locks, want 2", n)
	}
	if c.Device.Lock.LockCurrentState.Value() != 1 {
		t.Fatal("unenrolled lock unlocked")
	}
	a.Device.Lock.LockCurrentState.SetValue(1)
	b.Device.Lock.LockCurrentState.SetValue(1)
	if err := b.Store.Delete("phone.pairing"); err != nil {
		t.Fatal(err)
	}
	if n := unlockAuthorized(context.Background(), []*Lock{a, b, c}, snapshots, groupKey, result, "porch", 2); n != 1 {
		t.Fatalf("revoked lock authorized: %d", n)
	}
	if b.Device.Lock.LockCurrentState.Value() != 1 {
		t.Fatal("revoked lock unlocked")
	}
	a.Config.PhysicalLocks = []string{"sesame-missing"}
	a.Device.Lock.LockCurrentState.SetValue(1)
	if n := unlockAuthorized(context.Background(), []*Lock{a}, snapshots, groupKey, result, "porch", 3); n != 0 {
		t.Fatal("missing actuator controller reported success")
	}
	if a.Device.Lock.LockCurrentState.Value() != 1 {
		t.Fatal("fabricated physical unlock state")
	}
	a.Config.PhysicalLocks = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n := unlockAuthorized(ctx, []*Lock{a}, snapshots, groupKey, result, "porch", 3); n != 0 {
		t.Fatal("expired authentication unlocked")
	}
}
