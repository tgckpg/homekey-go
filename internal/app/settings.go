package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrConflict = errors.New("configuration changed; reload the page before saving")
var ErrReloading = errors.New("configuration is restarting; retry after the page reconnects")

type Settings struct {
	mu         sync.Mutex
	path       string
	config     Config
	restarting bool
	BeforeSave func(Config) error
	Reload     func()
}

func OpenSettings(path string, readOnly ...bool) (*Settings, error) {
	c, err := Load(path)
	if errors.Is(err, os.ErrNotExist) {
		c = Config{}
		c.Normalize()

		if len(readOnly) == 0 || !readOnly[0] {
			if err := writeConfig(path, c); err != nil {
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	}

	return &Settings{path: path, config: c}, nil
}
func revision(c Config) string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (s *Settings) Snapshot() (Config, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.config)
	var c Config
	_ = json.Unmarshal(b, &c)
	return c, revision(c)
}
func (s *Settings) Save(c Config, expected string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.restarting {
		return ErrReloading
	}
	if expected != revision(s.config) {
		return ErrConflict
	}
	c.Normalize()
	if err := c.Validate(); err != nil {
		return err
	}
	if s.BeforeSave != nil {
		if err := s.BeforeSave(c); err != nil {
			return err
		}
	}
	if err := writeConfig(s.path, c); err != nil {
		return err
	}
	s.config = c
	s.restarting = true
	return nil
}
func writeConfig(path string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err != nil {
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
		return fmt.Errorf("save configuration: %w", err)
	}
	if dir, e := os.Open(filepath.Dir(path)); e == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
