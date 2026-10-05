# Home Key Go on Home Assistant OS

## Configuration

Edit the app Configuration tab (YAML mode supports nested lists), save and
restart. There are no accessory-specific startup flags or persistent PIN option.

```yaml
interface: ""
locks:
  - id: GO-HOMEKEY-001
    name: Go Home Key
    port: 51826
    finish: silver
    readers: [porch]
  - id: back-door
    name: Back Door
    port: 51827
    finish: black
    readers: [porch]
readers:
  - id: porch
    address: "70:AF:09:16:42:5A"
    adapter: hci0
```

Each lock ID is immutable and supplies its serial. Name, finish, port and
reader assignments can be edited. Ports must be unique; 8099 is reserved for
the Web UI. A reader address can appear only once. IDs use letters, digits,
underscores and hyphens (1–64 characters). Use `readers: []` if no reader is
available yet. Use `locks: []` for an installation without locks.

Shared readers connect once and serve all assigned locks. Only locks that
actively enroll the authenticated Home Key endpoint unlock. The current ESP32
firmware supports one Home Key ECP group; assigned locks must share that group,
normally by pairing into the same Apple Home. Different groups reject taps.
Only one running service should connect to a reader. BlueZ adapter failures
reconnect independently of HomeKit.

## Pairing

Click **Open Web UI**, then **Pair** beside a lock. Add that named accessory in
Apple Home → Add Accessory → More Options, using the displayed code.
A window lasts five minutes. The code disappears after pairing or cancellation;
expiry and restarting also close setup. Pairing is rejected outside the window.
An already paired lock cannot open another setup window. To pair afresh, remove
it in Apple Home first. Existing connections use stored pairing keys.

The PIN is random for each service run and held only in memory. A repeated
window during the same run uses the same code. Codes are never printed to logs
or persisted. The Web UI is available through authenticated HA ingress only;
direct LAN access to port 8099 is rejected.

## Upgrading existing single-lock installations

Back up the app. Replace the old `name`, `serial`, `port`, `finish`, `pin`,
`ble_reader`, and `ble_adapter` options with the new structure. For the first
upgraded start, configure **exactly one lock**, using the old serial as its
`id`, old name, old finish, and old port. Define the reader separately and
assign its ID to this lock. Keep the complete `/data/state` directory.

The old root state is bound to that ID once, preserving HAP identity, pairing
and Home Key credentials. The obsolete saved PIN is deleted. Add further
locks after this first upgraded start. New lock state lives under
`/data/state/locks/<id>`. Keep IDs unchanged and preserve all state in backups.

Removing a lock from configuration stops serving it but preserves its state.
Adding its ID back restores its identity and credentials. Configuration edits
require restart. No MQTT, HA integration, or physical actuator is included yet.

## Local install and release

Run `sh scripts/stage-ha-app.sh`, copy `dist/homekey_go` into `/addons`, refresh
the HA app store, install and start it. Local staging removes the image field
so HA builds the included Dockerfile. Repository installations pull the image
matching the version in `config.yaml`; publish the 0.0.6 image before updating
that metadata in your app repository. Keep the app slug unchanged.

Standalone builds use `make`, copy `config.example.json` to `config.json`, then
run `./bin/homekey -config config.json`. The Web UI defaults to loopback port
8099. Linux host networking and host D-Bus are needed for container deployment
with BLE; supply a mounted JSON config and persistent state. With explicit
container arguments, include `-state /data/state`.

See [NFC.md](NFC.md) for firmware and phone testing.
