package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsImportNormalizeAndConflict(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "options.json")
	path := filepath.Join(dir, "config.json")
	os.WriteFile(seed, []byte(`{"interface":" ","locks":[{"id":"front ","name":"Front ","port":51826,"finish":"silver","readers":["TestReader01 "]}],"readers":[{"id":" TestReader01 ","address":"70:af:09:16:42:5a ","adapter":" hci0 "}]}`), 0600)
	s, err := OpenSettings(path, seed, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("status imported settings to disk")
	}
	s, err = OpenSettings(path, seed)
	if err != nil {
		t.Fatal(err)
	}
	c, rev := s.Snapshot()
	if c.Readers[0].Address != "70:AF:09:16:42:5A" || c.Readers[0].Adapter != "hci0" || c.Locks[0].Readers[0] != "TestReader01" {
		t.Fatalf("whitespace not normalized: %+v", c)
	}
	if err = s.Save(c, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision accepted")
	}
	c.Readers[0].Name = "Porch"
	if err = s.Save(c, rev); err != nil {
		t.Fatal(err)
	}
	if err = s.Save(c, rev); !errors.Is(err, ErrReloading) {
		t.Fatal("second save accepted during reload")
	}
	os.WriteFile(seed, []byte(`{"locks":[],"readers":[]}`), 0600)
	reopened, err := OpenSettings(path, seed)
	if err != nil {
		t.Fatal(err)
	}
	c, _ = reopened.Snapshot()
	if len(c.Readers) != 1 || c.Readers[0].Name != "Porch" {
		t.Fatal("Supervisor seed overwrote saved settings")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe config permissions")
	}
}
func TestConfigurationHTTPValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(path, Config{Locks: []LockConfig{}, Readers: []ReaderConfig{}})
	s, _ := OpenSettings(path, "")
	calls := 0
	s.Reload = func() { calls++ }
	handler := Handler(nil, nil, false, s)
	c, rev := s.Snapshot()
	send := func(header, revision string, c Config) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"config": c, "revision": revision})
		r := httptest.NewRequest("POST", "/api/config", bytes.NewReader(body))
		r.Header.Set("X-Homekey-Action", header)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := send("", rev, c); w.Code != 403 {
		t.Fatal("CSRF action header not enforced")
	}
	if w := send("configuration", "stale", c); w.Code != 409 {
		t.Fatal("stale settings not rejected")
	}
	c.Locks = append(c.Locks, LockConfig{ID: "front", Name: "Front", Port: 51826, Finish: "silver", Readers: []string{"missing"}})
	if w := send("configuration", rev, c); w.Code != 400 || calls != 0 {
		t.Fatal("invalid settings restarted service")
	}
	c.Locks[0].Readers = []string{}
	if w := send("configuration", rev, c); w.Code != 202 || calls != 1 {
		t.Fatalf("save failed %d %s", w.Code, w.Body.String())
	}
}
