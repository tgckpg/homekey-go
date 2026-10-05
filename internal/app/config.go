package app

import (
	"encoding/json"
	"fmt"
	"homekey.local/provisioner/internal/blegateway"
	"os"
	"regexp"
	"strings"
)

type LockConfig struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Port    int      `json:"port"`
	Finish  string   `json:"finish"`
	Readers []string `json:"readers"`
}
type ReaderConfig struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Adapter string `json:"adapter"`
}
type Config struct {
	Interface string         `json:"interface"`
	Locks     []LockConfig   `json:"locks"`
	Readers   []ReaderConfig `json:"readers"`
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	// Supervisor may retain removed legacy options. Only the new fields are used.
	if err = json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	readers := map[string]bool{}
	addresses := map[string]bool{}
	for _, r := range c.Readers {
		if !identifier.MatchString(r.ID) || readers[r.ID] {
			return fmt.Errorf("invalid or duplicate reader ID %q", r.ID)
		}
		if err := blegateway.ValidateAddress(r.Address); err != nil {
			return err
		}
		address := strings.ToUpper(r.Address)
		if addresses[address] {
			return fmt.Errorf("duplicate reader address %s", address)
		}
		if !regexp.MustCompile(`^hci[0-9]+$`).MatchString(r.Adapter) {
			return fmt.Errorf("invalid reader adapter")
		}
		readers[r.ID] = true
		addresses[address] = true
	}
	ids := map[string]bool{}
	ports := map[int]bool{}
	for _, l := range c.Locks {
		if !identifier.MatchString(l.ID) || ids[l.ID] {
			return fmt.Errorf("invalid or duplicate lock ID %q", l.ID)
		}
		if strings.TrimSpace(l.Name) == "" {
			return fmt.Errorf("lock name is required")
		}
		if l.Port < 1 || l.Port > 65535 || ports[l.Port] || l.Port == 8099 {
			return fmt.Errorf("invalid, reserved or duplicate lock port %d", l.Port)
		}
		switch l.Finish {
		case "silver", "black", "gold", "tan":
		default:
			return fmt.Errorf("invalid lock finish")
		}
		seen := map[string]bool{}
		for _, r := range l.Readers {
			if !readers[r] || seen[r] {
				return fmt.Errorf("unknown or duplicate reader %q for lock %s", r, l.ID)
			}
			seen[r] = true
		}
		ids[l.ID] = true
		ports[l.Port] = true
	}
	return nil
}
