// SPDX-License-Identifier: Apache-2.0
// Package physical owns enrolled actuator identities and their private keys.
package physical

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"homekey.local/provisioner/drivers/sesame"
)

type Record struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Driver string        `json:"driver"`
	Target sesame.Target `json:"target"`
}
type View struct {
	Record
	State sesame.State `json:"state"`
}
type Enrollment struct {
	Name   string        `json:"name"`
	Target sesame.Target `json:"target"`
	Mode   string        `json:"mode"`
	// Accept the SDK's toKeyJson fields, bound to the scanned device identity.
	Key *struct {
		UUID   string `json:"deviceUUID"`
		Model  string `json:"deviceModel"`
		Secret string `json:"secretKey"`
		Index  string `json:"keyIndex"`
		Public string `json:"sesame2PublicKey"`
	} `json:"key,omitempty"`
}
type Manager struct {
	ctx     context.Context
	cancel  context.CancelFunc
	dir     string
	dial    sesame.Dialer
	enroll  sync.Mutex
	mu      sync.RWMutex
	records map[string]Record
	clients map[string]*sesame.Client
	wg      sync.WaitGroup
}

func Open(ctx context.Context, dir string, dial sesame.Dialer) (*Manager, error) {
	if dial == nil {
		dial = sesame.DialBlueZ
	}
	life, cancel := context.WithCancel(ctx)
	m := &Manager{ctx: life, cancel: cancel, dir: dir, dial: dial, records: map[string]Record{}, clients: map[string]*sesame.Client{}}
	if e := os.MkdirAll(filepath.Join(dir, "physical-lock-keys"), 0700); e != nil {
		cancel()
		return nil, e
	}
	raw, e := os.ReadFile(filepath.Join(dir, "physical-locks.json"))
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		cancel()
		return nil, e
	}
	var records []Record
	if len(raw) > 0 {
		if e = json.Unmarshal(raw, &records); e != nil {
			cancel()
			return nil, e
		}
	}
	// Validate the whole registry before starting any device connections.
	keys := map[string]sesame.Credential{}
	for _, r := range records {
		id, err := identity(r.Target)
		if err != nil || id != r.ID || r.Driver != "sesame" || m.records[r.ID].ID != "" {
			cancel()
			return nil, errors.New("invalid physical lock registry")
		}
		c, e := m.readKey(id)
		if e != nil {
			cancel()
			return nil, fmt.Errorf("physical lock %s: %w", id, e)
		}
		keys[id] = c
		m.records[id] = r
	}
	for _, r := range records {
		m.start(r, keys[r.ID])
	}
	return m, nil
}
func identity(t sesame.Target) (string, error) {
	if e := sesame.ValidateTarget(t); e != nil {
		return "", e
	}
	uuid, e := sesame.NormalizeUUID(t.UUID)
	if e != nil {
		return "", e
	}
	return "sesame-" + uuid, nil
}
func (m *Manager) keyPath(id string) string {
	return filepath.Join(m.dir, "physical-lock-keys", id+".json")
}
func (m *Manager) readKey(id string) (sesame.Credential, error) {
	var c sesame.Credential
	b, e := os.ReadFile(m.keyPath(id))
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return c, e
	}
	return c, c.Validate()
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".physical-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(append(b, '\n')); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (m *Manager) start(r Record, key sesame.Credential) {
	c := sesame.NewClient(r.Target, key, m.dial)
	m.clients[r.ID] = c
	m.wg.Add(1)
	go func() { defer m.wg.Done(); c.Run(m.ctx) }()
}
func (m *Manager) Close() { m.cancel(); m.enroll.Lock(); defer m.enroll.Unlock(); m.wg.Wait() }
func (m *Manager) Has(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.records[id]
	return ok
}
func (m *Manager) Views() []View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]View, 0, len(m.records))
	for id, r := range m.records {
		out = append(out, View{r, m.clients[id].State()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (m *Manager) Add(ctx context.Context, in Enrollment) (Record, error) {
	m.enroll.Lock()
	defer m.enroll.Unlock()
	name := strings.TrimSpace(in.Name)
	if len(name) == 0 || len(name) > 128 {
		return Record{}, errors.New("physical lock name is required (maximum 128 bytes)")
	}
	uuid, e := sesame.NormalizeUUID(in.Target.UUID)
	if e != nil {
		return Record{}, e
	}
	in.Target.UUID = uuid
	id, e := identity(in.Target)
	if e != nil {
		return Record{}, e
	}
	if m.Has(id) {
		return Record{}, errors.New("physical lock is already added")
	}
	if in.Mode != "register" && in.Mode != "import" {
		return Record{}, errors.New("choose register or import")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	// Check writable private storage before starting irreversible registration.
	probe, e := os.CreateTemp(filepath.Join(m.dir, "physical-lock-keys"), ".probe-*")
	if e != nil {
		return Record{}, e
	}
	probe.Close()
	os.Remove(probe.Name())
	var key sesame.Credential
	if recovered, err := m.readKey(id); err == nil {
		// Retry can recover a key persisted before a failed registry write.
		key = recovered
		e = sesame.Probe(ctx, in.Target, key, m.dial)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Record{}, err
	} else if in.Mode == "import" {
		if in.Key == nil {
			return Record{}, errors.New("paste the Sesame key JSON")
		}
		keyUUID, err := sesame.NormalizeUUID(in.Key.UUID)
		if err != nil || keyUUID != uuid || in.Key.Model != in.Target.Model {
			return Record{}, errors.New("imported key does not match the selected lock")
		}
		key = sesame.Credential{Secret: strings.TrimSpace(in.Key.Secret)}
		if e = key.Validate(); e != nil {
			return Record{}, e
		}
		e = sesame.Probe(ctx, in.Target, key, m.dial)
		if e == nil {
			e = writeJSON(m.keyPath(id), key)
		}
	} else {
		e = sesame.Register(ctx, in.Target, m.dial, func(c sesame.Credential) error { key = c; return writeJSON(m.keyPath(id), c) })
	}
	if e != nil {
		return Record{}, e
	}
	in.Target.Registered = true
	r := Record{ID: id, Name: name, Driver: "sesame", Target: in.Target}
	m.mu.Lock()
	defer m.mu.Unlock()
	records := make([]Record, 0, len(m.records)+1)
	for _, v := range m.records {
		records = append(records, v)
	}
	records = append(records, r)
	if e = writeJSON(filepath.Join(m.dir, "physical-locks.json"), records); e != nil {
		return Record{}, fmt.Errorf("key saved, but physical lock list could not be saved; retry adding this device: %w", e)
	}
	m.records[id] = r
	m.start(r, key)
	return r, nil
}
func (m *Manager) Action(ctx context.Context, id, name string) error {
	m.mu.RLock()
	c := m.clients[id]
	m.mu.RUnlock()
	if c == nil {
		return errors.New("unknown physical lock")
	}
	return c.Action(ctx, name)
}

// Dispatch accepts an authorized command without holding the provision store's
// mutex through BLE I/O. Cancellation and a short deadline apply to the work.
func (m *Manager) Dispatch(ctx context.Context, ids []string, locked bool) <-chan error {
	result := make(chan error, 1)
	copyIDs := append([]string(nil), ids...)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		stop := context.AfterFunc(m.ctx, cancel)
		defer stop()
		name := "unlock"
		if locked {
			name = "lock"
		}
		var wg sync.WaitGroup
		errs := make(chan error, len(copyIDs))
		seen := map[string]bool{}
		for _, id := range copyIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				if e := m.Action(ctx, id, name); e != nil {
					errs <- fmt.Errorf("%s: %w", id, e)
				}
			}(id)
		}
		wg.Wait()
		close(errs)
		var all []error
		for e := range errs {
			all = append(all, e)
		}
		result <- errors.Join(all...)
	}()
	return result
}
