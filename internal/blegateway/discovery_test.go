package blegateway

import (
	"github.com/godbus/dbus/v5"
	"testing"
)

func TestDiscoveryFiltersReadersAndFindsAdapters(t *testing.T) {
	props := func(values map[string]any) map[string]dbus.Variant {
		p := map[string]dbus.Variant{}
		for k, v := range values {
			p[k] = dbus.MakeVariant(v)
		}
		return p
	}
	o := objects{
		"/org/bluez/hci1":                {"org.bluez.Adapter1": props(map[string]any{"Alias": "USB Bluetooth", "Address": "AA:00:00:00:00:01", "Powered": true})},
		"/org/bluez/hci0":                {"org.bluez.Adapter1": props(map[string]any{"Alias": "Internal", "Powered": false})},
		"/org/bluez/hci1/dev_reader":     {"org.bluez.Device1": props(map[string]any{"Name": "Test reader", "Address": "70:af:09:16:42:5a", "Adapter": dbus.ObjectPath("/org/bluez/hci1"), "UUIDs": []string{ServiceUUID}})},
		"/org/bluez/hci1/dev_headphones": {"org.bluez.Device1": props(map[string]any{"Name": "Headphones", "Address": "11:22:33:44:55:66", "Adapter": dbus.ObjectPath("/org/bluez/hci1")})},
		"/org/bluez/hci1/dev_known":      {"org.bluez.Device1": props(map[string]any{"Alias": "Configured reader", "Address": "AA:BB:CC:DD:EE:FF", "Adapter": dbus.ObjectPath("/org/bluez/hci1")})},
	}
	d := discoverySnapshot(o, map[string]bool{"AA:BB:CC:DD:EE:FF": true})
	if len(d.Adapters) != 2 || d.Adapters[0].ID != "hci0" || !d.Adapters[1].Powered {
		t.Fatalf("bad adapters: %+v", d.Adapters)
	}
	if len(d.Readers) != 2 || d.Readers[0].Address != "70:AF:09:16:42:5A" || d.Readers[0].Adapter != "hci1" {
		t.Fatalf("bad readers: %+v", d.Readers)
	}
	o["/org/bluez/hci1/service"] = map[string]map[string]dbus.Variant{"org.bluez.GattService1": props(map[string]any{"UUID": ServiceUUID, "Device": dbus.ObjectPath("/org/bluez/hci1/dev_known")})}
	if d := discoverySnapshot(o, nil); len(d.Readers) != 2 {
		t.Fatal("known GATT service not detected")
	}
}
