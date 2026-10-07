# Home Key Go on Home Assistant OS

## Configuration

Open **Open Web UI**. HA's static Configuration form has been replaced by the
Go configuration editor; it cannot populate dynamic Bluetooth choices.

1. Click **Add reader**. Bluetooth discovery lists Home Key readers with their
   names, addresses, and adapters. Choose a device; its adapter is filled in.
2. Give the reader a friendly name. Its immutable ID is generated automatically.
3. Click **Add lock** or **Edit** beside an existing lock. Select its readers
   using checkboxes; no IDs need to be typed. A lock can use several readers,
   and a reader can be shared by several locks in the same Home Key group.
4. Click **Save configuration**. The service saves atomically and briefly
   restarts its HomeKit/BLE connections. The page reconnects automatically.
   Existing lock IDs, pairing identities and credentials stay unchanged.

**Scan Bluetooth** refreshes discovery. Only devices advertising the relay
service, already exposing its GATT service, or previously configured readers
are listed. Power on the ESP32 reader and local Bluetooth adapter first.
Discovery does not connect to, pair with, or disconnect devices, and it does
not change adapter settings. An off adapter is shown as off.

If discovery is unavailable, **Enter address manually** remains available.
Adapter choices use detected adapters and existing configured adapters; when
none are known, the default hci0 is shown explicitly as unverified. Addresses
and IDs are trimmed before validation, and addresses are normalized to uppercase.

Names can be edited without changing IDs. HomeKit port and Wallet artwork are
under **HomeKit settings**; the port defaults to the next free configured
number. Every lock needs a unique port, and 8099 is reserved for the Web UI.
New ports are checked for conflicts before saving. **Network settings** allows
LAN interface selection; leave it empty for automatic selection.

Removing a reader removes its draft lock assignments. Removing a lock stops
serving it after save but leaves its credential state on disk. Changes remain
in the browser until Save configuration. Concurrent edits from another page
are rejected rather than overwriting a newer configuration.

## Physical locks (Sesame)

1. In **Physical locks**, click **Add physical lock**. Scan results show supported
   nearby Sesame locks and their local Bluetooth adapters.
2. For an unregistered lock, choose **Register an unregistered lock**. This
   provisions BLE access and saves its key immediately. It does not reset an
   existing lock or enroll the lock in a Sesame cloud account.
3. For a lock already configured in the Sesame app, choose **Import an existing
   key** and paste the SDK key JSON containing `deviceUUID`, `deviceModel`, and
   `secretKey` (the SDK also exports `keyIndex` and `sesame2PublicKey`). The key must
   match the selected device and pass a local login. Cloud-only guest keys do
   not work. This is not a parser for arbitrary Sesame sharing QR codes.
4. Open **Set positions**. With the door open, turn the thumb-turn to the desired
   locked position, wait for the displayed position to update, and click
   **Use current as locked**. Repeat for unlocked. Set the boundary the same way;
   its button is enabled only when firmware reports support. Changes are saved
   directly to the lock. Use **Test lock** / **Test unlock** to verify movement.
5. Edit a Home Key lock and select its **Assigned physical locks**, alongside its
   assigned readers. Save configuration. Several virtual locks can share one
   physical lock; a tap dispatches at most one unlock per physical device.

Physical-lock enrollment and calibration take effect immediately. Virtual-lock
assignments remain drafts until **Save configuration**. No physical assignment
means simulation mode. Home Key success means the lock accepted the unlock
command; motor completion is reported separately from real state notifications.
Offline, moving, critical or conflicting multi-lock states appear as Unknown.

The controller uses direct BlueZ on the local adapter. The server must be in BLE
range; HA Bluetooth proxies and ESP32 lock tunnelling are not included. Connections
are kept open for prompt commands and manual state updates; battery impact and
phone-app coexistence need testing on the actual lock.

The physical registry is `/data/state/physical-locks.json`; private keys are under
`/data/state/physical-lock-keys/` with restrictive permissions. Back up the entire
state directory. Keys are excluded from public configuration/status responses.
If enrollment saved a key but failed to save registry metadata, retry adding the
same device to recover it. To remove a physical lock, first unassign it from all Home Key locks and save
configuration, then click **Remove** in Physical locks. Removal stops its BLE
connection and deletes its local saved key immediately. The Sesame lock is not
reset and its calibration is unchanged; adding it again requires a usable key
or resetting and registering the lock again.

## Updating from 0.0.6

The first start imports existing locks, reader IDs, addresses, adapters and
assignments from `/data/options.json` into `/data/state/config.json`.
Subsequent starts load that saved configuration; Supervisor options do not
replace it. Keep all of `/data/state` in backups. Reader names initially use
existing IDs until renamed in the UI. Pairing state remains in its existing
`/data/state/locks/<id>` directory.

Fresh installations start empty. Add readers and locks through Open Web UI.
The current ESP32 firmware advertises one Home Key ECP group; shared readers
must serve locks in the same group, normally the same Apple Home. Different
groups reject taps. Only assigned locks with active enrollment unlock.

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

## Local install and release

Run `sh scripts/stage-ha-app.sh`, copy `dist/homekey_go` into `/addons`, refresh
the HA app store, install and start it. Local staging removes the image field
so HA builds the included Dockerfile. Repository installations pull the image
matching the version in `config.yaml`; publish the 0.0.7 image before updating
that metadata in your app repository. Keep the app slug unchanged.

Standalone builds use `make`, copy `config.example.json` to `config.json`, then
run `./bin/homekey -config config.json`. The Web UI defaults to loopback port
8099. Linux host networking and host D-Bus are needed for container deployment
with BLE; supply a mounted JSON config and persistent state. With explicit
container arguments, include `-state /data/state`.

See [NFC.md](NFC.md) for firmware and phone testing.
