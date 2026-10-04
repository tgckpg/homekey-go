package hkserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"homekey.local/provisioner/internal/provision"
)

func testDevice(t *testing.T) *Device {
	t.Helper()
	s, e := provision.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	pub := bytes.Repeat([]byte{42}, 32)
	p, _ := json.Marshal(struct {
		Name       string
		PublicKey  []byte
		Permission byte
	}{"phone", pub, 1})
	if e = s.Set("phone.pairing", p); e != nil {
		t.Fatal(e)
	}
	d, e := New(s, Config{Name: "Test Lock", Serial: "test", Finish: "silver", PIN: "51808582", Address: ":51826"})
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func TestAccessoryAndProvisioningRetry(t *testing.T) {
	d := testDevice(t)
	b, e := json.Marshal(d.Accessory)
	if e != nil {
		t.Fatal(e)
	}
	var a struct {
		Services []struct {
			Type            string `json:"type"`
			Characteristics []struct {
				Type  string   `json:"type"`
				Perms []string `json:"perms"`
				Value any      `json:"value"`
			} `json:"characteristics"`
		} `json:"services"`
	}
	if e = json.Unmarshal(b, &a); e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for _, s := range a.Services {
		seen["service:"+s.Type] = true
		for _, c := range s.Characteristics {
			seen[c.Type] = true
			if c.Type == "265" && c.Value != "AQEQAgEQ" {
				t.Fatalf("supported configuration = %v", c.Value)
			}
			if c.Type == "26C" && c.Value != base64.StdEncoding.EncodeToString([]byte{1, 4, 0xe3, 0xe3, 0xe3, 0}) {
				t.Fatal("bad finish")
			}
		}
	}
	for _, k := range []string{"service:266", "service:45", "service:44", "263", "264", "265", "26C"} {
		if !seen[k] {
			t.Fatal("missing", k)
		}
	}
	key := make([]byte, 32)
	key[31] = 1
	body := append(provision.TLV(1, []byte{2}), provision.TLV(2, key)...)
	body = append(body, provision.TLV(3, []byte("reader01"))...)
	raw := append(provision.TLV(1, []byte{2}), provision.TLV(6, body)...)
	encoded := base64.StdEncoding.EncodeToString(raw)
	req := httptest.NewRequest(http.MethodPut, "/characteristics", nil)
	for i, want := range [][]byte{{7, 3, 2, 1, 0}, {7, 3, 2, 1, 2}} {
		v, code := d.Control.SetValueRequest(encoded, req)
		if code != 0 || v != base64.StdEncoding.EncodeToString(want) {
			t.Fatalf("write %d: %v %d", i, v, code)
		}
		if d.Control.Value() != "" {
			t.Fatal("private request retained as characteristic value")
		}
	}
	v, code := d.Control.ValueRequest(req)
	if code != 0 || v != "" {
		t.Fatal("GET leaked command payload")
	}
	if _, code := d.Control.SetValueRequest("not base64!", req); code == 0 {
		t.Fatal("invalid base64 accepted")
	}
}
func TestUnauthenticatedHTTPDenied(t *testing.T) {
	d := testDevice(t)
	mux, ok := d.Server.ServeMux().(http.Handler)
	if !ok {
		t.Fatal("mux is not a handler")
	}
	for _, path := range []string{"/accessories", "/characteristics?id=1.1"} {
		r := httptest.NewRecorder()
		mux.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		if !bytes.Contains(r.Body.Bytes(), []byte("-70401")) {
			t.Fatalf("request not rejected: %d %s", r.Code, r.Body.String())
		}
	}
}
