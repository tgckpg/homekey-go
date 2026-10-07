package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homekey.local/provisioner/drivers/sesame"
	"homekey.local/provisioner/internal/physical"
)

func TestPhysicalHTTPGuardsAndEmptyRegistry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, e := physical.Open(ctx, t.TempDir(), func(context.Context, sesame.Target) (sesame.Link, sesame.Target, error) {
		t.Fatal("unexpected Bluetooth action")
		return nil, sesame.Target{}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	s, e := OpenSettings(filepath.Join(t.TempDir(), "config.json"))
	if e != nil {
		t.Fatal(e)
	}
	s.Physical = m
	h := Handler(nil, nil, false, s)
	for _, tc := range []struct {
		method, path, body, header string
		want                       int
	}{{"GET", "/api/physical-locks", "", "", 200}, {"POST", "/api/physical-locks", "{}", "", 403}, {"POST", "/api/physical-locks", "{} {}", "physical-lock", 400}, {"POST", "/api/physical-locks", "{\"secret\":\"bad\"}", "physical-lock", 400}, {"POST", "/api/physical-locks/unknown/unlock", "", "", 403}, {"POST", "/api/physical-locks/unknown/unlock", "", "physical-lock", 404}, {"POST", "/api/physical-locks/unknown/reset", "", "physical-lock", 404}} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("X-Homekey-Action", tc.header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	c, rev := s.Snapshot()
	c.Locks = []LockConfig{{ID: "front", Name: "Front", Port: 51826, Finish: "silver", PhysicalLocks: []string{"sesame-missing"}}}
	if e = s.Save(c, rev); e == nil {
		t.Fatal("accepted unregistered assignment")
	}
}
func TestAggregatePhysicalState(t *testing.T) {
	p := int16(90)
	good := sesame.State{Online: true, Position: &p, Stopped: true, Locked: true}
	states := map[string]sesame.State{"a": good, "b": good}
	if aggregatePhysical([]string{"a", "b"}, states) != 1 {
		t.Fatal("locked")
	}
	good.Locked = false
	states["b"] = good
	if aggregatePhysical([]string{"a", "b"}, states) != 3 {
		t.Fatal("mixed state must be unknown")
	}
	if aggregatePhysical([]string{"b"}, states) != 0 {
		t.Fatal("unlocked")
	}
	good.Online = false
	states["b"] = good
	if aggregatePhysical([]string{"b"}, states) != 3 {
		t.Fatal("offline state must be unknown")
	}
	good.Online = true
	good.Stopped = false
	states["b"] = good
	if aggregatePhysical([]string{"b"}, states) != 3 {
		t.Fatal("moving state must be unknown")
	}
}

func TestRemovePhysicalHTTPRejectsAssignmentsAndDeletesKey(t *testing.T) {
	dir := t.TempDir()
	target := sesame.Target{UUID: "00112233-4455-6677-8899-aabbccddeeff", Model: "sesame_6_pro", Adapter: "hci0", Registered: true}
	id := "sesame-" + target.UUID
	keyDir := filepath.Join(dir, "physical-lock-keys")
	os.Mkdir(keyDir, 0700)
	key := filepath.Join(keyDir, id+".json")
	os.WriteFile(key, []byte(`{"secret":"000102030405060708090a0b0c0d0e0f"}`), 0600)
	raw, _ := json.Marshal([]physical.Record{{ID: id, Name: "Door", Driver: "sesame", Target: target}})
	os.WriteFile(filepath.Join(dir, "physical-locks.json"), raw, 0600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m, e := physical.Open(ctx, dir, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Config{Locks: []LockConfig{{ID: "front", Name: "Front", Port: 51826, Finish: "silver", PhysicalLocks: []string{id}}}}
	writeConfig(path, cfg)
	s, e := OpenSettings(path)
	if e != nil {
		t.Fatal(e)
	}
	s.Physical = m
	send := func(header string) int {
		r := httptest.NewRequest("DELETE", "/api/physical-locks/"+id, nil)
		r.Header.Set("X-Homekey-Action", header)
		w := httptest.NewRecorder()
		Handler(nil, nil, false, s).ServeHTTP(w, r)
		return w.Code
	}
	if code := send(""); code != 403 {
		t.Fatal("removal missing action header", code)
	}
	if code := send("physical-lock"); code != 409 {
		t.Fatal("removed assigned lock", code)
	}
	if !m.Has(id) {
		t.Fatal("assigned lock forgotten")
	}
	cfg.Locks[0].PhysicalLocks = nil
	writeConfig(path, cfg)
	s, e = OpenSettings(path)
	if e != nil {
		t.Fatal(e)
	}
	s.Physical = m
	if code := send("physical-lock"); code != 204 {
		t.Fatal("removal failed", code)
	}
	if _, e := os.Stat(key); !os.IsNotExist(e) {
		t.Fatal("saved key not deleted")
	}
	if code := send("physical-lock"); code != 404 {
		t.Fatal("unknown physical lock removal", code)
	}
}
