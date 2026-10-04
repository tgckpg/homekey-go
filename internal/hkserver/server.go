// SPDX-License-Identifier: Apache-2.0
package hkserver

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"

	"github.com/brutella/hap"
	"github.com/brutella/hap/accessory"
	"github.com/brutella/hap/characteristic"
	"github.com/brutella/hap/service"
	"homekey.local/provisioner/internal/provision"
)

type Config struct {
	Name, Serial, Finish, PIN, Address string
	Interfaces                         []string
}
type Device struct {
	Server    *hap.Server
	Accessory *accessory.A
	Control   *characteristic.C
	Lock      *service.LockMechanism
}

func bytesCharacteristic(t string, raw []byte, permissions ...string) *characteristic.C {
	c := characteristic.NewBytes(t)
	c.Permissions = permissions
	c.SetValue(raw)
	return c.C
}

func New(store *provision.Store, cfg Config) (*Device, error) {
	colors := map[string]string{"black": "00000000", "tan": "ced5da00", "gold": "aad6ec00", "silver": "e3e3e300"}
	color, ok := colors[cfg.Finish]
	if !ok {
		return nil, fmt.Errorf("finish must be black, tan, gold, or silver")
	}
	finish, _ := hex.DecodeString(color)
	// HAP category 6 is a standalone door lock, not a bridge.
	a := accessory.New(accessory.Info{Name: cfg.Name, SerialNumber: cfg.Serial, Manufacturer: "DIY", Model: "Go Home Key", Firmware: "0.1.0"}, 6)
	a.IdentifyFunc = func(*http.Request) { log.Print("Identify: virtual Home Key lock") }
	a.Info.AddC(bytesCharacteristic("26C", provision.TLV(1, finish), characteristic.PermissionRead))
	lock := service.NewLockMechanism()
	lock.Primary = true
	lock.LockCurrentState.SetValue(1)
	lock.LockTargetState.SetValue(1)
	lock.LockTargetState.ValidVals = []int{0, 1}
	lock.LockTargetState.OnSetRemoteValue(func(v int) error {
		log.Printf("Virtual lock state: %d (simulation; no physical lock connected)", v)
		return lock.LockCurrentState.SetValue(v)
	})
	a.AddS(lock.S)
	management := service.NewLockManagement()
	management.Version.SetValue("1.0")
	management.LockControlPoint.SetValue([]byte{})
	management.LockControlPoint.Stateless = true
	management.LockControlPoint.SetValueRequestFunc = func(any, *http.Request) (any, int) { return nil, -70406 }
	a.AddS(management.S)
	nfc := service.New("266")
	state := characteristic.NewInt("263")
	state.Format = characteristic.FormatUInt16
	state.Permissions = []string{characteristic.PermissionRead, characteristic.PermissionEvents}
	state.SetValue(0) // Matches the reference implementation; semantics not established.
	nfc.AddC(state.C)
	control := bytesCharacteristic("264", []byte{}, characteristic.PermissionRead, characteristic.PermissionWrite, characteristic.PermissionWriteResponse)
	control.Stateless = true
	control.ValueRequestFunc = func(*http.Request) (any, int) { return "", 0 }
	control.SetValueRequestFunc = func(value any, _ *http.Request) (any, int) {
		encoded, ok := value.(string)
		if !ok || len(encoded) > base64.StdEncoding.EncodedLen(provision.MaxMessage) {
			return nil, -70410
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return nil, -70410
		}
		response, err := store.Handle(raw)
		if err != nil {
			// No raw TLVs, keys, public keys or credential payloads in logs.
			log.Printf("Provisioning request rejected: %v", err)
			return nil, -70402
		}
		v := store.Summary()
		log.Printf("Provisioning exchange complete: reader=%t issuers=%d endpoints=%d", v.ReaderProvisioned, v.Issuers, v.Endpoints)
		return base64.StdEncoding.EncodeToString(response), 0
	}
	nfc.AddC(control)
	supported := append(provision.TLV(1, []byte{provision.Capacity}), provision.TLV(2, []byte{provision.Capacity})...)
	nfc.AddC(bytesCharacteristic("265", supported, characteristic.PermissionRead))
	a.AddS(nfc)
	s, err := hap.NewServer(store, a)
	if err != nil {
		return nil, err
	}
	s.Pin = cfg.PIN
	s.Addr = cfg.Address
	s.Ifaces = cfg.Interfaces
	return &Device{Server: s, Accessory: a, Control: control, Lock: lock}, nil
}
