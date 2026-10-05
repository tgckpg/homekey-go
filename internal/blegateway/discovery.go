package blegateway

import (
	"context"
	"fmt"
	"github.com/godbus/dbus/v5"
	"sort"
	"strings"
	"time"
)

type AdapterInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Powered bool   `json:"powered"`
}
type ReaderInfo struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Adapter string `json:"adapter"`
}
type Discovery struct {
	Adapters []AdapterInfo `json:"adapters"`
	Readers  []ReaderInfo  `json:"readers"`
	Warnings []string      `json:"warnings"`
}

func discoverySnapshot(o objects, known map[string]bool) Discovery {
	d := Discovery{Adapters: []AdapterInfo{}, Readers: []ReaderInfo{}, Warnings: []string{}}
	services := map[dbus.ObjectPath]bool{}
	for _, ifs := range o {
		p, ok := ifs["org.bluez.GattService1"]
		if ok && strings.EqualFold(text(p, "UUID"), ServiceUUID) {
			owner, _ := p["Device"].Value().(dbus.ObjectPath)
			services[owner] = true
		}
	}
	for path, ifs := range o {
		if p, ok := ifs["org.bluez.Adapter1"]; ok {
			powered, _ := p["Powered"].Value().(bool)
			name := text(p, "Alias")
			if name == "" {
				name = text(p, "Name")
			}
			id := string(path)
			id = id[strings.LastIndex(id, "/")+1:]
			d.Adapters = append(d.Adapters, AdapterInfo{ID: id, Name: name, Address: text(p, "Address"), Powered: powered})
		}
		if p, ok := ifs["org.bluez.Device1"]; ok {
			address := strings.ToUpper(text(p, "Address"))
			match := known[address] || services[path]
			uuids, _ := p["UUIDs"].Value().([]string)
			for _, u := range uuids {
				if strings.EqualFold(u, ServiceUUID) {
					match = true
				}
			}
			if !match {
				continue
			}
			adapter, _ := p["Adapter"].Value().(dbus.ObjectPath)
			id := string(adapter)
			id = id[strings.LastIndex(id, "/")+1:]
			name := text(p, "Alias")
			if name == "" {
				name = text(p, "Name")
			}
			if name == "" {
				name = "Home Key reader"
			}
			d.Readers = append(d.Readers, ReaderInfo{Name: name, Address: address, Adapter: id})
		}
	}
	sort.Slice(d.Adapters, func(i, j int) bool { return d.Adapters[i].ID < d.Adapters[j].ID })
	sort.Slice(d.Readers, func(i, j int) bool {
		a, b := d.Readers[i], d.Readers[j]
		return a.Adapter+a.Address < b.Adapter+b.Address
	})
	return d
}

// Discover uses a separate BlueZ connection and owns only its own discovery
// sessions. It never pairs, connects, disconnects, or changes adapter settings.
func Discover(ctx context.Context, known []string) (Discovery, error) {
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return Discovery{}, fmt.Errorf("Bluetooth unavailable: %w", err)
	}
	defer c.Close()
	o, err := getObjects(ctx, c)
	if err != nil {
		return Discovery{}, err
	}
	addresses := map[string]bool{}
	for _, a := range known {
		addresses[strings.ToUpper(strings.TrimSpace(a))] = true
	}
	initial := discoverySnapshot(o, addresses)
	var scans []dbus.BusObject
	warnings := []string{}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		for _, a := range scans {
			_ = a.CallWithContext(cleanup, "org.bluez.Adapter1.StopDiscovery", 0).Err
		}
	}()
	for _, a := range initial.Adapters {
		if !a.Powered {
			warnings = append(warnings, a.ID+" is switched off")
			continue
		}
		obj := c.Object("org.bluez", dbus.ObjectPath("/org/bluez/"+a.ID))
		if e := obj.CallWithContext(ctx, "org.bluez.Adapter1.StartDiscovery", 0).Err; e != nil {
			warnings = append(warnings, a.ID+": "+e.Error())
			continue
		}
		scans = append(scans, obj)
	}
	if len(scans) > 0 && !wait(ctx, 5*time.Second) {
		return Discovery{}, ctx.Err()
	}
	o, err = getObjects(ctx, c)
	if err != nil {
		return Discovery{}, err
	}
	d := discoverySnapshot(o, addresses)
	d.Warnings = warnings
	if len(d.Adapters) == 0 {
		d.Warnings = append(d.Warnings, "No local Bluetooth adapters found")
	}
	return d, nil
}
