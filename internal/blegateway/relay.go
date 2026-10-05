// SPDX-License-Identifier: Apache-2.0
package blegateway

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
)

const StatusUUID = "7a6b0003-5b21-4f36-8e5d-9c3a26d74210"
const APDUUUID = "7a6b0004-5b21-4f36-8e5d-9c3a26d74210"
const headerSize = 12
const maxAPDU = 240

type Card struct {
	Session uint32
	UID     []byte
	SAK     byte
}
type Handler func(context.Context, Card, *Relay) error

// The gateway goroutine serializes all requests, including PING and release.
type Relay struct {
	characteristic dbus.BusObject
	session        uint32
	seq            uint16
	writeSize      int
}

func packet(kind byte, session uint32, seq uint16, offset, total int, data []byte) []byte {
	b := make([]byte, headerSize+len(data))
	b[0] = 1
	b[1] = kind
	binary.BigEndian.PutUint32(b[2:], session)
	binary.BigEndian.PutUint16(b[6:], seq)
	binary.BigEndian.PutUint16(b[8:], uint16(offset))
	binary.BigEndian.PutUint16(b[10:], uint16(total))
	copy(b[12:], data)
	return b
}
func parseCard(b []byte) (Card, error) {
	if len(b) != 18 || b[0] != 1 || b[1] > 1 {
		return Card{}, fmt.Errorf("invalid NFC status")
	}
	if b[1] == 0 {
		return Card{}, nil
	}
	n := int(b[2])
	if n != 4 && n != 7 && n != 10 {
		return Card{}, fmt.Errorf("invalid NFC UID length")
	}
	sid := binary.BigEndian.Uint32(b[4:])
	if sid == 0 {
		return Card{}, fmt.Errorf("invalid NFC session")
	}
	return Card{Session: sid, SAK: b[3], UID: append([]byte(nil), b[8:8+n]...)}, nil
}
func parseResponse(b []byte, sid uint32, seq uint16) ([]byte, bool, error) {
	if len(b) == 0 {
		return nil, false, nil
	}
	if len(b) < headerSize || b[0] != 1 {
		return nil, false, fmt.Errorf("invalid relay response")
	}
	if binary.BigEndian.Uint32(b[2:]) != sid || binary.BigEndian.Uint16(b[6:]) != seq {
		return nil, false, nil
	}
	n := int(binary.BigEndian.Uint16(b[10:]))
	if n > maxAPDU || len(b) != headerSize+n || binary.BigEndian.Uint16(b[8:]) != 0 {
		return nil, false, fmt.Errorf("invalid relay response length")
	}
	switch b[1] {
	case 0x10:
		if n != 0 {
			return nil, false, fmt.Errorf("invalid pending response")
		}
		return nil, false, nil
	case 0x11:
		if n < 2 {
			return nil, false, fmt.Errorf("missing APDU status")
		}
		return append([]byte(nil), b[headerSize:]...), true, nil
	case 0x12:
		if n != 1 {
			return nil, false, fmt.Errorf("invalid relay error")
		}
		return nil, false, fmt.Errorf("PN532 exchange failed (code %d)", b[headerSize])
	default:
		return nil, false, fmt.Errorf("unknown relay response")
	}
}
func (r *Relay) write(ctx context.Context, b []byte) error {
	return r.characteristic.CallWithContext(ctx, "org.bluez.GattCharacteristic1.WriteValue", 0, b, map[string]dbus.Variant{"type": dbus.MakeVariant("request")}).Err
}
func (r *Relay) Exchange(ctx context.Context, apdu []byte) ([]byte, error) {
	if len(apdu) < 4 || len(apdu) > maxAPDU {
		return nil, fmt.Errorf("APDU length outside 4..%d", maxAPDU)
	}
	if r.seq == 65535 {
		return nil, fmt.Errorf("APDU sequence exhausted")
	}
	r.seq++
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	limit := r.writeSize - headerSize
	if limit < 1 {
		return nil, fmt.Errorf("BLE MTU too small")
	}
	for off := 0; off < len(apdu); {
		end := off + limit
		if end > len(apdu) {
			end = len(apdu)
		}
		if err := r.write(ctx, packet(1, r.session, r.seq, off, len(apdu), apdu[off:end])); err != nil {
			return nil, err
		}
		off = end
	}
	for ctx.Err() == nil {
		var b []byte
		if err := r.characteristic.CallWithContext(ctx, "org.bluez.GattCharacteristic1.ReadValue", 0, map[string]dbus.Variant{}).Store(&b); err != nil {
			return nil, err
		}
		data, ready, err := parseResponse(b, r.session, r.seq)
		if err != nil || ready {
			return data, err
		}
		if !wait(ctx, 20*time.Millisecond) {
			break
		}
	}
	return nil, ctx.Err()
}
func (r *Relay) Release(ctx context.Context) error {
	return r.write(ctx, packet(2, r.session, 0, 0, 0, nil))
}
func readCard(ctx context.Context, ch dbus.BusObject) (Card, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var b []byte
	if err := ch.CallWithContext(ctx, "org.bluez.GattCharacteristic1.ReadValue", 0, map[string]dbus.Variant{}).Store(&b); err != nil {
		return Card{}, err
	}
	return parseCard(b)
}
