// SPDX-License-Identifier: Apache-2.0
// Package blegateway implements the initial Linux BlueZ PING/PONG probe.
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
func Run(ctx context.Context, address, adapter string, interval time.Duration) {
	for ctx.Err() == nil {
		if err := session(ctx, strings.ToUpper(address), adapter, interval); err != nil && ctx.Err() == nil {
			log.Printf("BLE gateway: %v; retrying in 5s", err)
		}
		if !wait(ctx, 5*time.Second) {
			return
		}
	}
}
func session(ctx context.Context, address, adapter string, interval time.Duration) error {
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
	log.Printf("BLE gateway: connected to %s", address)
	ch := c.Object("org.bluez", characteristic)
	for seq := uint32(1); ctx.Err() == nil; seq++ {
		pingCtx, pingDone := context.WithTimeout(ctx, 5*time.Second)
		started := time.Now()
		err := ch.CallWithContext(pingCtx, "org.bluez.GattCharacteristic1.WriteValue", 0,
			request(seq), map[string]dbus.Variant{"type": dbus.MakeVariant("request")}).Err
		var reply []byte
		if err == nil {
			err = ch.CallWithContext(pingCtx, "org.bluez.GattCharacteristic1.ReadValue", 0, map[string]dbus.Variant{}).Store(&reply)
		}
		pingDone()
		if err != nil {
			return fmt.Errorf("PING %d: %w", seq, err)
		}
		if !validReply(reply, seq) {
			return fmt.Errorf("PING %d: unexpected reply %x", seq, reply)
		}
		log.Printf("BLE PONG reader=%s seq=%d round_trip=%s", address, seq, time.Since(started).Round(time.Millisecond))
		if !wait(ctx, interval) {
			return nil
		}
	}
	return nil
}
