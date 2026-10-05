// SPDX-License-Identifier: Apache-2.0
// Package blegateway implements the Linux BlueZ NFC APDU relay and PING/PONG probe.
package blegateway

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const ServiceUUID = "7a6b0001-5b21-4f36-8e5d-9c3a26d74210"
const PingUUID = "7a6b0002-5b21-4f36-8e5d-9c3a26d74210"

type objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// ValidateAddress deliberately requires an explicit reader, rather than
// connecting to whichever device happens to advertise our service first.
func ValidateAddress(address string) error {
	a, err := net.ParseMAC(address)
	if err != nil || len(a) != 6 {
		return fmt.Errorf("BLE address must be six colon-separated octets")
	}
	if strings.Count(address, ":") != 5 {
		return fmt.Errorf("BLE address must use colon separators")
	}
	return nil
}
func request(seq uint32) []byte {
	b := make([]byte, 8)
	copy(b, "PING")
	binary.BigEndian.PutUint32(b[4:], seq)
	return b
}
func validReply(b []byte, seq uint32) bool {
	return len(b) == 8 && bytes.Equal(b[:4], []byte("PONG")) && binary.BigEndian.Uint32(b[4:]) == seq
}
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func getObjects(ctx context.Context, c *dbus.Conn) (objects, error) {
	var o objects
	err := c.Object("org.bluez", "/").CallWithContext(ctx, "org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&o)
	return o, err
}
func text(p map[string]dbus.Variant, k string) string { v, _ := p[k].Value().(string); return v }

// Run reconnects independently of HAP. A Bluetooth failure does not stop
// HomeKit provisioning. Each attempt gets a fresh system-bus connection.
func Run(ctx context.Context, address, adapter string, interval time.Duration, handler Handler, groupProvider func() []byte) {
	for ctx.Err() == nil {
		if err := session(ctx, strings.ToUpper(address), adapter, interval, handler, groupProvider); err != nil && ctx.Err() == nil {
			log.Printf("BLE gateway: %v; retrying in 5s", err)
		}
		if !wait(ctx, 5*time.Second) {
			return
		}
	}
}
func session(ctx context.Context, address, adapter string, interval time.Duration, handler Handler, groupProvider func() []byte) error {
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("system D-Bus: %w", err)
	}
	defer c.Close()
	adapterPath := dbus.ObjectPath("/org/bluez/" + adapter)
	a := c.Object("org.bluez", adapterPath)
	// BlueZ reference-counts discovery sessions; stop only our own session.
	scanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = a.CallWithContext(scanCtx, "org.bluez.Adapter1.StartDiscovery", 0).Err
	cancel()
	if err != nil {
		return fmt.Errorf("start discovery on %s: %w", adapter, err)
	}
	stopScan := func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = a.CallWithContext(cleanup, "org.bluez.Adapter1.StopDiscovery", 0).Err
	}
	scanning := true
	defer func() {
		if scanning {
			stopScan()
		}
	}()
	log.Printf("BLE gateway: discovering %s on %s", address, adapter)
	discoverCtx, done := context.WithTimeout(ctx, 30*time.Second)
	defer done()
	var device dbus.ObjectPath
	for device == "" {
		o, e := getObjects(discoverCtx, c)
		if e != nil {
			return e
		}
		for p, interfaces := range o {
			props, ok := interfaces["org.bluez.Device1"]
			if ok && strings.HasPrefix(string(p), string(adapterPath)+"/") && strings.EqualFold(text(props, "Address"), address) {
				device = p
				break
			}
		}
		if device == "" && !wait(discoverCtx, 250*time.Millisecond) {
			return fmt.Errorf("reader discovery: %w", discoverCtx.Err())
		}
	}
	d := c.Object("org.bluez", device)
	connectCtx, connectDone := context.WithTimeout(ctx, 20*time.Second)
	err = d.CallWithContext(connectCtx, "org.bluez.Device1.Connect", 0).Err
	connectDone()
	if err != nil {
		return fmt.Errorf("connect %s: %w", address, err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = d.CallWithContext(cleanup, "org.bluez.Device1.Disconnect", 0).Err
	}()
	stopScan()
	scanning = false
	resolveCtx, resolveDone := context.WithTimeout(ctx, 20*time.Second)
	defer resolveDone()
	var characteristic dbus.ObjectPath
	for characteristic == "" {
		o, e := getObjects(resolveCtx, c)
		if e != nil {
			return e
		}
		resolved, _ := o[device]["org.bluez.Device1"]["ServicesResolved"].Value().(bool)
		if resolved {
			for p, interfaces := range o {
				props, ok := interfaces["org.bluez.GattCharacteristic1"]
				if !ok || !strings.EqualFold(text(props, "UUID"), PingUUID) {
					continue
				}
				service, _ := props["Service"].Value().(dbus.ObjectPath)
				sp := o[service]["org.bluez.GattService1"]
				owner, _ := sp["Device"].Value().(dbus.ObjectPath)
				if owner == device && strings.EqualFold(text(sp, "UUID"), ServiceUUID) {
					characteristic = p
					break
				}
			}
		}
		if characteristic == "" && !wait(resolveCtx, 250*time.Millisecond) {
			return fmt.Errorf("PING service resolution: %w", resolveCtx.Err())
		}
	}
	o, err := getObjects(ctx, c)
	if err != nil {
		return err
	}
	var statusPath, apduPath dbus.ObjectPath
	writeSize := 20 // Works even at the mandatory ATT MTU of 23.
	for path, ifs := range o {
		props, ok := ifs["org.bluez.GattCharacteristic1"]
		if !ok {
			continue
		}
		service, _ := props["Service"].Value().(dbus.ObjectPath)
		sp := o[service]["org.bluez.GattService1"]
		owner, _ := sp["Device"].Value().(dbus.ObjectPath)
		if owner != device || !strings.EqualFold(text(sp, "UUID"), ServiceUUID) {
			continue
		}
		switch strings.ToLower(text(props, "UUID")) {
		case StatusUUID:
			statusPath = path
		case APDUUUID:
			apduPath = path
			if mtu, ok := props["MTU"].Value().(uint16); ok && mtu >= 23 {
				writeSize = int(mtu) - 3
				if writeSize > 244 {
					writeSize = 244
				}
			}
		}
	}
	if statusPath == "" || apduPath == "" {
		return fmt.Errorf("NFC relay characteristics missing; flash the matching ESP32 firmware")
	}
	log.Printf("BLE gateway: connected to %s; APDU write_size=%d", address, writeSize)
	ch := c.Object("org.bluez", characteristic)
	status := c.Object("org.bluez", statusPath)
	apdu := c.Object("org.bluez", apduPath)
	var configuredGroup []byte
	configured := false
	var lastSession uint32
	seq := uint32(0)
	nextPing := time.Time{}
	for ctx.Err() == nil {
		card, e := readCard(ctx, status)
		if e != nil {
			return fmt.Errorf("NFC status: %w", e)
		}
		// Configure only between sessions; the NFC owner takes a snapshot for
		// each scan. Every new BLE connection gets a fresh configuration.
		if card.Session == 0 && groupProvider != nil {
			group := groupProvider()
			if !configured || !bytes.Equal(group, configuredGroup) {
				b, err := ecpConfigPacket(group)
				if err != nil {
					return err
				}
				configCtx, done := context.WithTimeout(ctx, 3*time.Second)
				err = (&Relay{characteristic: apdu}).write(configCtx, b)
				done()
				if err != nil {
					return fmt.Errorf("ECP configuration (flash matching firmware): %w", err)
				}
				configuredGroup = append([]byte(nil), group...)
				configured = true
				log.Printf("BLE gateway: Home Key ECP configured reader=%s enabled=%t", address, len(group) == 8)
			}
		}
		if card.Session != 0 && card.Session != lastSession {
			lastSession = card.Session
			relay := &Relay{characteristic: apdu, session: card.Session, writeSize: writeSize}
			authCtx, done := context.WithTimeout(ctx, 12*time.Second)
			if handler != nil {
				e = handler(authCtx, card, relay)
			}
			done()
			cleanup, finish := context.WithTimeout(context.Background(), 3*time.Second)
			releaseError := relay.Release(cleanup)
			finish()
			if e != nil {
				log.Printf("NFC authentication rejected reader=%s session=%d: %v", address, card.Session, e)
			}
			if releaseError != nil && ctx.Err() == nil {
				log.Printf("NFC release: %v", releaseError)
			}
		}
		if !time.Now().Before(nextPing) {
			seq++
			pingCtx, pingDone := context.WithTimeout(ctx, 5*time.Second)
			started := time.Now()
			err := ch.CallWithContext(pingCtx, "org.bluez.GattCharacteristic1.WriteValue", 0, request(seq), map[string]dbus.Variant{"type": dbus.MakeVariant("request")}).Err
			var reply []byte
			if err == nil {
				err = ch.CallWithContext(pingCtx, "org.bluez.GattCharacteristic1.ReadValue", 0, map[string]dbus.Variant{}).Store(&reply)
			}
			pingDone()
			if err != nil {
				return fmt.Errorf("PING %d: %w", seq, err)
			}
			if !validReply(reply, seq) {
				return fmt.Errorf("PING %d: unexpected reply", seq)
			}
			log.Printf("BLE PONG reader=%s seq=%d round_trip=%s", address, seq, time.Since(started).Round(time.Millisecond))
			nextPing = time.Now().Add(interval)
		}
		if !wait(ctx, 100*time.Millisecond) {
			return nil
		}
	}
	return nil
}
