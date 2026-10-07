// SPDX-License-Identifier: Apache-2.0
package sesame

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

type Link interface {
	Write(context.Context, []byte) error
	Packets() <-chan []byte
	Done() <-chan error
	Close()
}
type Dialer func(context.Context, Target) (Link, Target, error)
type Adapter struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Powered bool   `json:"powered"`
}
type Discovery struct {
	Adapters []Adapter `json:"adapters"`
	Locks    []Target  `json:"locks"`
	Warnings []string  `json:"warnings"`
}
type objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant

func getObjects(ctx context.Context, c *dbus.Conn) (objects, error) {
	var o objects
	e := c.Object("org.bluez", "/").CallWithContext(ctx, "org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&o)
	return o, e
}
func value(p map[string]dbus.Variant, k string) string { s, _ := p[k].Value().(string); return s }

var adapterPattern = regexp.MustCompile(`^hci[0-9]+$`)

func ValidateTarget(t Target) error {
	if _, e := NormalizeUUID(t.UUID); e != nil {
		return e
	}
	if !Supported(t.Model) {
		return errors.New("unsupported Sesame model")
	}
	if !adapterPattern.MatchString(t.Adapter) {
		return errors.New("invalid Bluetooth adapter")
	}
	return nil
}
func advertised(p map[string]dbus.Variant) (Target, bool) {
	// BlueZ strips the two-byte company identifier. Do not accept arbitrary
	// manufacturer payloads merely because their first byte matches a model.
	md, ok := p["ManufacturerData"].Value().(map[uint16]dbus.Variant)
	if !ok {
		return Target{}, false
	}
	v, ok := md[0x055a]
	if !ok {
		return Target{}, false
	}
	b, ok := v.Value().([]byte)
	if !ok {
		return Target{}, false
	}
	t, ok := Advertisement(b)
	if !ok {
		return Target{}, false
	}
	a, _ := p["Adapter"].Value().(dbus.ObjectPath)
	parts := strings.Split(string(a), "/")
	t.Adapter = parts[len(parts)-1]
	t.Address = value(p, "Address")
	return t, true
}
func snapshot(o objects) Discovery {
	d := Discovery{Adapters: []Adapter{}, Locks: []Target{}, Warnings: []string{}}
	for path, ifs := range o {
		if p, ok := ifs["org.bluez.Adapter1"]; ok {
			v, _ := p["Powered"].Value().(bool)
			parts := strings.Split(string(path), "/")
			d.Adapters = append(d.Adapters, Adapter{parts[len(parts)-1], value(p, "Alias"), v})
		}
		if p, ok := ifs["org.bluez.Device1"]; ok {
			if t, ok := advertised(p); ok {
				d.Locks = append(d.Locks, t)
			}
		}
	}
	sort.Slice(d.Adapters, func(i, j int) bool { return d.Adapters[i].ID < d.Adapters[j].ID })
	sort.Slice(d.Locks, func(i, j int) bool { return d.Locks[i].Adapter+d.Locks[i].UUID < d.Locks[j].Adapter+d.Locks[j].UUID })
	return d
}
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func stopScan(a dbus.BusObject) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = a.CallWithContext(ctx, "org.bluez.Adapter1.StopDiscovery", 0).Err
}
func Discover(ctx context.Context) (Discovery, error) {
	c, e := dbus.ConnectSystemBus()
	if e != nil {
		return Discovery{}, fmt.Errorf("Bluetooth unavailable: %w", e)
	}
	defer c.Close()
	o, e := getObjects(ctx, c)
	if e != nil {
		return Discovery{}, e
	}
	d := snapshot(o)
	var active []dbus.BusObject
	defer func() {
		for _, a := range active {
			stopScan(a)
		}
	}()
	for _, a := range d.Adapters {
		if !a.Powered {
			d.Warnings = append(d.Warnings, a.ID+" is switched off")
			continue
		}
		obj := c.Object("org.bluez", dbus.ObjectPath("/org/bluez/"+a.ID))
		if e := obj.CallWithContext(ctx, "org.bluez.Adapter1.StartDiscovery", 0).Err; e != nil {
			d.Warnings = append(d.Warnings, a.ID+": "+e.Error())
		} else {
			active = append(active, obj)
		}
	}
	if len(active) > 0 {
		if e := pause(ctx, 5*time.Second); e != nil {
			return Discovery{}, e
		}
	}
	o, e = getObjects(ctx, c)
	if e != nil {
		return Discovery{}, e
	}
	out := snapshot(o)
	out.Warnings = d.Warnings
	if len(out.Adapters) == 0 {
		out.Warnings = append(out.Warnings, "No local Bluetooth adapters found")
	}
	return out, nil
}

// Choose the most recently advertised identity, rather than an arbitrary
// cached address. BlueZ can retain several Device1 entries for one Sesame UUID.
func chooseTarget(o objects, t Target, seen map[dbus.ObjectPath]uint64) (dbus.ObjectPath, Target) {
	var selected dbus.ObjectPath
	var found Target
	for path, ifs := range o {
		candidate, ok := advertised(ifs["org.bluez.Device1"])
		if !ok || candidate.UUID != t.UUID || candidate.Adapter != t.Adapter {
			continue
		}
		if selected == "" || seen[path] > seen[selected] || (seen[path] == seen[selected] && string(path) < string(selected)) {
			selected, found = path, candidate
		}
	}
	return selected, found
}

// StartDiscovery is reference counted. These signal matches and the scan belong
// only to this D-Bus client; do not remove BlueZ devices or reset the adapter.
func findTarget(ctx context.Context, c *dbus.Conn, t Target) (dbus.ObjectPath, Target, error) {
	adapterPath := dbus.ObjectPath("/org/bluez/" + t.Adapter)
	signals := make(chan *dbus.Signal, 256)
	c.Signal(signals)
	defer c.RemoveSignal(signals)
	properties := []dbus.MatchOption{dbus.WithMatchSender("org.bluez"), dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchMember("PropertiesChanged"), dbus.WithMatchPathNamespace(adapterPath)}
	added := []dbus.MatchOption{dbus.WithMatchSender("org.bluez"), dbus.WithMatchInterface("org.freedesktop.DBus.ObjectManager"), dbus.WithMatchMember("InterfacesAdded")}
	if e := c.AddMatchSignal(properties...); e != nil {
		return "", Target{}, e
	}
	defer c.RemoveMatchSignal(properties...)
	if e := c.AddMatchSignal(added...); e != nil {
		return "", Target{}, e
	}
	defer c.RemoveMatchSignal(added...)
	adapter := c.Object("org.bluez", adapterPath)
	if e := adapter.CallWithContext(ctx, "org.bluez.Adapter1.StartDiscovery", 0).Err; e != nil {
		return "", Target{}, fmt.Errorf("start Sesame scan on %s: %w", t.Adapter, e)
	}
	defer stopScan(adapter)
	seen := map[dbus.ObjectPath]uint64{}
	var sequence uint64
	started := time.Now()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", Target{}, fmt.Errorf("discover Sesame %s on %s: %w", t.UUID, t.Adapter, ctx.Err())
		case sig, ok := <-signals:
			if !ok {
				return "", Target{}, errors.New("Bluetooth bus closed during discovery")
			}
			if sig == nil || len(sig.Body) < 2 {
				continue
			}
			var path dbus.ObjectPath
			switch sig.Name {
			case "org.freedesktop.DBus.Properties.PropertiesChanged":
				props, ok := sig.Body[1].(map[string]dbus.Variant)
				if !ok || sig.Body[0] != "org.bluez.Device1" {
					continue
				}
				_, manufacturer := props["ManufacturerData"]
				_, rssi := props["RSSI"]
				if !manufacturer && !rssi {
					continue
				}
				path = sig.Path
			case "org.freedesktop.DBus.ObjectManager.InterfacesAdded":
				path, _ = sig.Body[0].(dbus.ObjectPath)
				ifs, ok := sig.Body[1].(map[string]map[string]dbus.Variant)
				if !ok {
					continue
				}
				if _, ok := ifs["org.bluez.Device1"]; !ok {
					continue
				}
			default:
				continue
			}
			if strings.HasPrefix(string(path), string(adapterPath)+"/") {
				sequence++
				seen[path] = sequence
			}
		case <-ticker.C:
			o, e := getObjects(ctx, c)
			if e != nil {
				return "", Target{}, fmt.Errorf("read Sesame scan: %w", e)
			}
			path, found := chooseTarget(o, t, seen)
			// Give new advertisements a chance to replace pre-reboot cached entries.
			// A cache fallback still works when BlueZ suppresses duplicate reports.
			if path != "" && (seen[path] > 0 || time.Since(started) >= 2*time.Second) {
				if found.Model != t.Model {
					return "", Target{}, errors.New("Sesame model changed; scan again")
				}
				connected, _ := o[path]["org.bluez.Device1"]["Connected"].Value().(bool)
				if connected {
					return "", Target{}, errors.New("Sesame is already connected through this adapter")
				}
				return path, found, nil
			}
		}
	}
}

type bluezLink struct {
	conn          *dbus.Conn
	device, write dbus.BusObject
	mode          string
	packets       chan []byte
	done          chan error
	cancel        context.CancelFunc
	once          sync.Once
}

func (b *bluezLink) Packets() <-chan []byte { return b.packets }
func (b *bluezLink) Done() <-chan error     { return b.done }
func (b *bluezLink) Write(ctx context.Context, data []byte) error {
	return b.write.CallWithContext(ctx, "org.bluez.GattCharacteristic1.WriteValue", 0, data, map[string]dbus.Variant{"type": dbus.MakeVariant(b.mode)}).Err
}
func (b *bluezLink) Close() {
	b.once.Do(func() {
		b.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = b.device.CallWithContext(ctx, "org.bluez.Device1.Disconnect", 0).Err
		b.conn.Close()
	})
}
func DialBlueZ(ctx context.Context, t Target) (Link, Target, error) {
	if e := ValidateTarget(t); e != nil {
		return nil, Target{}, e
	}
	c, e := dbus.ConnectSystemBus()
	if e != nil {
		return nil, Target{}, e
	}
	success := false
	var device dbus.BusObject
	owned := false
	defer func() {
		if !success {
			if owned {
				cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = device.CallWithContext(cleanup, "org.bluez.Device1.Disconnect", 0).Err
			}
			c.Close()
		}
	}()
	path, found, e := findTarget(ctx, c, t)
	if e != nil {
		return nil, Target{}, e
	}
	device = c.Object("org.bluez", path)
	owned = true
	if e = device.CallWithContext(ctx, "org.bluez.Device1.Connect", 0).Err; e != nil {
		return nil, Target{}, fmt.Errorf("connect Sesame %s on %s: %w", found.Address, t.Adapter, e)
	}
	var writePath, notifyPath dbus.ObjectPath
	mode := ""
	for writePath == "" || notifyPath == "" {
		o, e := getObjects(ctx, c)
		if e != nil {
			return nil, Target{}, fmt.Errorf("discover Sesame GATT services at %s: %w", found.Address, e)
		}
		resolved, _ := o[path]["org.bluez.Device1"]["ServicesResolved"].Value().(bool)
		if resolved {
			for p, ifs := range o {
				v, ok := ifs["org.bluez.GattCharacteristic1"]
				if !ok {
					continue
				}
				sp, _ := v["Service"].Value().(dbus.ObjectPath)
				service := o[sp]["org.bluez.GattService1"]
				owner, _ := service["Device"].Value().(dbus.ObjectPath)
				if owner != path || !strings.EqualFold(value(service, "UUID"), ServiceUUID) {
					continue
				}
				switch strings.ToLower(value(v, "UUID")) {
				case WriteUUID:
					flags, _ := v["Flags"].Value().([]string)
					for _, f := range flags {
						if f == "write" && mode == "" {
							mode = "request"
						}
						if f == "write-without-response" {
							mode = "command"
						}
					}
					writePath = p
				case NotifyUUID:
					notifyPath = p
				}
			}
		}
		if writePath == "" || notifyPath == "" {
			if e = pause(ctx, 100*time.Millisecond); e != nil {
				return nil, Target{}, fmt.Errorf("discover Sesame GATT services at %s: %w", found.Address, e)
			}
		}
	}
	if mode == "" {
		return nil, Target{}, errors.New("Sesame command characteristic is not writable")
	}
	signals := make(chan *dbus.Signal, 256)
	c.Signal(signals)
	if e = c.AddMatchSignal(dbus.WithMatchSender("org.bluez"), dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchMember("PropertiesChanged"), dbus.WithMatchPathNamespace(path)); e != nil {
		return nil, Target{}, e
	}
	life, cancel := context.WithCancel(context.Background())
	b := &bluezLink{conn: c, device: device, write: c.Object("org.bluez", writePath), mode: mode, packets: make(chan []byte, 128), done: make(chan error, 1), cancel: cancel}
	go func() {
		fail := func(e error) {
			select {
			case b.done <- e:
			default:
			}
		}
		defer c.RemoveSignal(signals)
		for {
			select {
			case <-life.Done():
				return
			case sig, ok := <-signals:
				if !ok {
					fail(errors.New("Bluetooth bus disconnected"))
					return
				}
				if sig == nil || len(sig.Body) < 2 {
					continue
				}
				props, ok := sig.Body[1].(map[string]dbus.Variant)
				if !ok {
					continue
				}
				if sig.Path == path {
					if v, exists := props["Connected"]; exists {
						on, _ := v.Value().(bool)
						if !on {
							fail(errors.New("Sesame disconnected"))
							return
						}
					}
				}
				if sig.Path == notifyPath {
					if data, ok := props["Value"].Value().([]byte); ok {
						select {
						case b.packets <- append([]byte(nil), data...):
						default:
							fail(errors.New("Sesame notification queue overflow"))
							return
						}
					}
				}
			}
		}
	}()
	if e = c.Object("org.bluez", notifyPath).CallWithContext(ctx, "org.bluez.GattCharacteristic1.StartNotify", 0).Err; e != nil {
		cancel()
		return nil, Target{}, fmt.Errorf("subscribe to Sesame notifications at %s: %w", found.Address, e)
	}
	success = true
	return b, found, nil
}
