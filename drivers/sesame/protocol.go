// SPDX-License-Identifier: Apache-2.0
// Protocol adapted from CANDY HOUSE's MIT-licensed Sesame Android SDK.
package sesame

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ServiceUUID = "0000fd81-0000-1000-8000-00805f9b34fb"
	WriteUUID   = "16860002-a5ae-9856-b6d3-dbb4c676993e"
	NotifyUUID  = "16860003-a5ae-9856-b6d3-dbb4c676993e"
	maxMessage  = 1024
)

type Target struct {
	UUID       string `json:"uuid"`
	Model      string `json:"model"`
	Adapter    string `json:"adapter"`
	Address    string `json:"address"`
	Registered bool   `json:"registered"`
}

// Credential is private state. Never include it in public configuration/status.
type Credential struct {
	Secret string `json:"secret"`
}

func (c Credential) key() ([]byte, error) {
	b, e := hex.DecodeString(c.Secret)
	if e != nil || len(b) != 16 {
		return nil, errors.New("Sesame secret must contain exactly 32 hexadecimal characters")
	}
	return b, nil
}
func (c Credential) Validate() error { _, e := c.key(); return e }

func NormalizeUUID(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return "", errors.New("invalid Sesame UUID")
	}
	b, e := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if e != nil || len(b) != 16 {
		return "", errors.New("invalid Sesame UUID")
	}
	return formatUUID(b), nil
}
func formatUUID(b []byte) string {
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func model(product byte) string {
	switch product {
	case 5:
		return "sesame_5"
	case 7:
		return "sesame_5_pro"
	case 20:
		return "sesame_6"
	case 21:
		return "sesame_6_pro"
	}
	return ""
}
func Supported(s string) bool {
	for _, p := range []byte{5, 7, 20, 21} {
		if model(p) == s {
			return true
		}
	}
	return false
}
func Advertisement(b []byte) (Target, bool) {
	if len(b) < 19 || model(b[0]) == "" {
		return Target{}, false
	}
	return Target{UUID: formatUUID(b[3:19]), Model: model(b[0]), Registered: b[2]&1 != 0}, true
}

type State struct {
	Online          bool      `json:"online"`
	Error           string    `json:"error,omitempty"`
	Position        *int16    `json:"position,omitempty"`
	Target          *int16    `json:"target,omitempty"`
	Locked          bool      `json:"locked"`
	Stopped         bool      `json:"stopped"`
	Critical        bool      `json:"critical"`
	BatteryCritical bool      `json:"battery_critical"`
	LockPosition    *int16    `json:"lock_position,omitempty"`
	UnlockPosition  *int16    `json:"unlock_position,omitempty"`
	Boundary        *int16    `json:"boundary,omitempty"`
	Updated         time.Time `json:"updated"`
}

func i16(b []byte) *int16 { v := int16(binary.LittleEndian.Uint16(b)); return &v }
func short(v int16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(v))
	return b
}
func (s *State) publish(item byte, b []byte) error {
	switch item {
	case 81:
		if len(b) < 7 {
			return errors.New("short mechanical status")
		}
		s.Position = i16(b[4:6])
		s.Target = i16(b[2:4])
		if *s.Target == -32768 {
			s.Target = nil
		}
		s.Locked = b[6]&2 != 0
		s.Critical = b[6]&8 != 0
		s.Stopped = b[6]&16 != 0
		s.BatteryCritical = b[6]&32 != 0
	case 80:
		if len(b) < 6 {
			return errors.New("short mechanical settings")
		}
		s.LockPosition = i16(b[:2])
		s.UnlockPosition = i16(b[2:4])
	case 20:
		if len(b) != 2 {
			return errors.New("invalid lock boundary")
		}
		s.Boundary = i16(b)
	}
	s.Updated = time.Now()
	return nil
}

type assembler struct {
	data   []byte
	active bool
}

func (a *assembler) feed(b []byte) (byte, []byte, error) {
	if len(b) < 2 || len(b) > 20 || b[0] > 5 {
		return 0, nil, errors.New("invalid Sesame fragment")
	}
	start, kind := b[0]&1 != 0, b[0]>>1
	if start {
		if a.active {
			return 0, nil, errors.New("interrupted Sesame message")
		}
		a.data = nil
		a.active = true
	} else if !a.active {
		return 0, nil, errors.New("orphan Sesame fragment")
	}
	if len(a.data)+len(b)-1 > maxMessage {
		return 0, nil, errors.New("Sesame message too large")
	}
	a.data = append(a.data, b[1:]...)
	if kind == 0 {
		return 0, nil, nil
	}
	out := a.data
	a.data = nil
	a.active = false
	return kind, out, nil
}
func fragments(kind byte, b []byte) ([][]byte, error) {
	if (kind != 1 && kind != 2) || len(b) == 0 || len(b) > maxMessage {
		return nil, errors.New("invalid Sesame message")
	}
	var out [][]byte
	start := byte(1)
	for len(b) > 0 {
		n := len(b)
		if n > 19 {
			n = 19
		}
		h := start
		if n == len(b) {
			h |= kind << 1
		}
		f := append([]byte{h}, b[:n]...)
		out = append(out, f)
		b = b[n:]
		start = 0
	}
	return out, nil
}

type ResultError struct{ Item, Code byte }

func (e *ResultError) Error() string {
	return fmt.Sprintf("Sesame rejected command 0x%02x (result %d)", e.Item, e.Code)
}
