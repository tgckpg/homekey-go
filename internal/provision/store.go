// SPDX-License-Identifier: Apache-2.0
package provision

import (
	"bytes"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type Endpoint struct {
	PublicKey     string `json:"public_key"`
	KeyType       byte   `json:"key_type"`
	KeyState      byte   `json:"key_state"`
	EnrolledAt    int64  `json:"enrolled_at"`
	PersistentKey string `json:"persistent_key,omitempty"` // populated by future NFC authentication
}
type Issuer struct {
	PublicKey string              `json:"public_key"`
	Endpoints map[string]Endpoint `json:"endpoints"`
}
type Credentials struct {
	ReaderPrivateKey string            `json:"reader_private_key,omitempty"`
	ReaderIdentifier string            `json:"reader_identifier,omitempty"`
	Issuers          map[string]Issuer `json:"issuers"`
}
type Data struct {
	Version int               `json:"version"`
	HAP     map[string][]byte `json:"hap"`
	HomeKey Credentials       `json:"homekey"`
}

// Store implements hap.Store. Pairings and Home Key credentials are committed
// together, so unpairing revokes the corresponding issuer in the same update.
// One process must own a store; the CLI takes an OS file lock before opening it.
type Store struct {
	mu   sync.Mutex
	path string
	data Data
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "state.json"), data: Data{Version: 1, HAP: map[string][]byte{}, HomeKey: Credentials{Issuers: map[string]Issuer{}}}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&s.data); err != nil {
		return nil, fmt.Errorf("invalid state file; refusing to reset: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("trailing content in state file")
	}
	if s.data.Version != 1 || s.data.HAP == nil || s.data.HomeKey.Issuers == nil {
		return nil, fmt.Errorf("invalid state schema")
	}
	if err := validateCredentials(s.data.HomeKey); err != nil {
		return nil, err
	}
	if err := reconcile(&s.data); err != nil {
		return nil, err
	}
	if err := os.Chmod(s.path, 0600); err != nil {
		return nil, err
	}
	return s, nil
}

func keyID(b []byte) string {
	h := sha256.New()
	h.Write([]byte("key-identifier"))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func reconcile(d *Data) error {
	current := make(map[string]Issuer)
	for name, raw := range d.HAP {
		if !strings.HasSuffix(name, ".pairing") {
			continue
		}
		var p struct {
			Name       string
			PublicKey  []byte
			Permission byte
		}
		if err := json.Unmarshal(raw, &p); err != nil || len(p.PublicKey) != 32 || p.Name == "" {
			return fmt.Errorf("invalid stored HAP pairing")
		}
		id := keyID(p.PublicKey)
		issuer, ok := d.HomeKey.Issuers[id]
		if !ok {
			issuer = Issuer{PublicKey: hex.EncodeToString(p.PublicKey), Endpoints: map[string]Endpoint{}}
		}
		current[id] = issuer
	}
	if len(current) > Capacity {
		return fmt.Errorf("issuer capacity exceeded")
	}
	d.HomeKey.Issuers = current
	if len(current) == 0 {
		d.HomeKey.ReaderPrivateKey = ""
		d.HomeKey.ReaderIdentifier = ""
	}
	return nil
}

func validateCredentials(c Credentials) error {
	if c.ReaderPrivateKey != "" {
		p, e := hex.DecodeString(c.ReaderPrivateKey)
		if e != nil || !validScalar(p) {
			return fmt.Errorf("invalid saved reader key")
		}
		id, e := hex.DecodeString(c.ReaderIdentifier)
		if e != nil || len(id) != 8 {
			return fmt.Errorf("invalid saved reader identifier")
		}
	} else if c.ReaderIdentifier != "" {
		return fmt.Errorf("reader identifier without key")
	}
	for id, i := range c.Issuers {
		p, e := hex.DecodeString(i.PublicKey)
		if e != nil || len(p) != 32 || id != keyID(p) || i.Endpoints == nil {
			return fmt.Errorf("invalid saved issuer")
		}
		for eid, ep := range i.Endpoints {
			p, e := hex.DecodeString(ep.PublicKey)
			if e != nil || len(p) != 65 {
				return fmt.Errorf("invalid saved endpoint")
			}
			x, _ := elliptic.Unmarshal(elliptic.P256(), p)
			if x == nil || eid != endpointID(p) || ep.KeyType != 2 || ep.KeyState > 1 {
				return fmt.Errorf("invalid saved endpoint")
			}
			if ep.PersistentKey != "" {
				k, e := hex.DecodeString(ep.PersistentKey)
				if e != nil || len(k) != 32 {
					return fmt.Errorf("invalid saved persistent key")
				}
			}
		}
	}
	return nil
}

func (s *Store) update(fn func(*Data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Clone before mutation. Failed validation or persistence leaves live state intact.
	old, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	var next Data
	if err = json.Unmarshal(old, &next); err != nil {
		return err
	}
	if err = fn(&next); err != nil {
		return err
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if bytes.Equal(b, old) {
		return nil
	}
	if err = atomicWrite(s.path, b); err != nil {
		return err
	}
	s.data = next
	return nil
}

func atomicWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	// Best effort directory fsync: rename is the commit point. Reporting a later
	// failure as rollback would leave memory inconsistent with the committed file.
	if dir, e := os.Open(filepath.Dir(path)); e == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
func (s *Store) Set(key string, value []byte) error {
	return s.update(func(d *Data) error {
		d.HAP[key] = append([]byte(nil), value...)
		if strings.HasSuffix(key, ".pairing") {
			return reconcile(d)
		}
		return nil
	})
}
func (s *Store) Get(key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data.HAP[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), v...), nil
}
func (s *Store) Delete(key string) error {
	return s.update(func(d *Data) error {
		if _, ok := d.HAP[key]; !ok {
			return os.ErrNotExist
		}
		delete(d.HAP, key)
		if strings.HasSuffix(key, ".pairing") {
			return reconcile(d)
		}
		return nil
	})
}
func (s *Store) KeysWithSuffix(suffix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for key := range s.data.HAP {
		if strings.HasSuffix(key, suffix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

type Summary struct {
	PairedControllers int  `json:"paired_controllers"`
	ReaderProvisioned bool `json:"reader_provisioned"`
	Issuers           int  `json:"issuers"`
	Endpoints         int  `json:"endpoints"`
}

func (s *Store) Summary() Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := Summary{ReaderProvisioned: s.data.HomeKey.ReaderPrivateKey != "", Issuers: len(s.data.HomeKey.Issuers)}
	for k := range s.data.HAP {
		if strings.HasSuffix(k, ".pairing") {
			v.PairedControllers++
		}
	}
	for _, i := range s.data.HomeKey.Issuers {
		v.Endpoints += len(i.Endpoints)
	}
	return v
}
