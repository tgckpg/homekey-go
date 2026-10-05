# Home Key in Go

Basically [kormax/apple-home-key-reader](https://github.com/kormax/apple-home-key-reader) in go.

## Configuration and pairing

Build with Go 1.23+ using `make`. Copy [config.example.json](config.example.json)
to `config.json`, edit the locks/readers, then run:

```sh
./bin/homekey -config config.json -state ./state
```

Open http://127.0.0.1:8099 and click **Pair** for a lock. In Apple Home use
**Add Accessory → More Options**, select its name and enter the code. The
five-minute pairing window closes when paired, canceled, expired, or restarted.
The code is generated in memory for each service run, never logged or saved,
and hidden after pairing. Reopening a window during the same run uses the same
code. Existing pairings use their stored keys and need no setup PIN.

In HA OS, edit the app **Configuration** tab and restart, then use **Open Web UI**
for pairing. See [HA app instructions](ha-app/DOCS.md).

Every lock has an immutable `id`, editable `name`, unique HomeKit TCP `port`,
Wallet artwork `finish`, and a list of reader IDs. IDs allow letters, digits,
underscores and hyphens (1–64 characters); they also supply stable serials.
Keep an ID unchanged once paired. Each lock is a standalone HomeKit accessory
with its own identity, pairing secrets and Home Key credentials.

Readers have an `id`, ESP32 BLE `address`, and BlueZ `adapter` such as `hci0`.
One reader can be assigned to several locks, and a lock can use several readers.
Only assigned locks with an active enrollment for the authenticated endpoint
unlock. Shared readers currently require a common Home Key group (normally
locks in the same Apple Home); different groups disable ECP and reject taps.
This firmware advertises one group. Multi-group firmware support is separate work.

The key artwork may be determined by the first Home Key lock in a Home;
editing a finish may not change existing Wallet artwork.

```text
-config     Lock/reader configuration JSON (default: ./config.json)
-state      Persistent directory (default: ./state)
-web-addr   Pairing UI address (default: 127.0.0.1:8099)
-ingress    Restrict UI access to the HA ingress gateway
-status     Print credential counts per configured lock and exit
```

Accessory-specific CLI flags have been removed. LAN interface selection is
in the config JSON. HomeKit still needs TCP access to each lock's port and
local mDNS UDP 5353. On macOS use the native process for discovery; BLE uses
Linux BlueZ. The virtual states do not operate a physical lock.

## What successful provisioning looks like

Logs contain summaries such as:

```text
Provisioning exchange complete: reader=true issuers=1 endpoints=1
```

You can inspect the current counts without displaying any keys:

```sh
./bin/homekey -config config.json -status -state ./state
```

Example:

```json
{"front-door":{"paired_controllers":1,"reader_provisioned":true,"issuers":1,"endpoints":1}}
```

- `paired_controllers > 0`: HomeKit pairing completed.
- `reader_provisioned = true`: the controller supplied the reader identity/key.
- `endpoints > 0`: at least one phone/watch public credential was enrolled.
- The actual Home Key appearing in Wallet is the final confirmation on iOS.

## Persistence and lifecycle

`state/locks/<id>/state.json` contains **both HAP pairing secrets and Home Key credentials**.
Keep the whole directory across restarts and back it up securely. Directory
permissions are 0700 and state files are 0600. Writes use a synced temporary
file plus atomic rename; directory syncing is best effort. A process lock
prevents two servers from accidentally sharing the same state directory.

The reader private key is provided during HomeKit provisioning; this program
does not invent a separate key and expect Wallet to accept it. Issuer public
keys are derived from HAP pairing records. Provisioned endpoint public keys
are validated on P-256 and saved under the corresponding issuer.

Removing a HAP pairing removes that issuer and its enrolled endpoints in the
same state update. Removing the last pairing also clears the reader key.
Re-adding an identical credential returns Duplicate. Disk errors reject the
operation without updating live state. Corrupt saved state stops startup
rather than silently losing the pairing.

No private keys, credential TLVs or endpoint public keys are logged. The
setup PIN is only displayed during a pairing window in the Web UI. Do not
turn on the HAP library's raw debug logging when using real credentials.

For upgrades from the single-lock version, first configure exactly one lock
with its original serial as `id`, its original name, finish, and HAP port.
The service binds the existing root `state.json` to this ID using a durable
`legacy-lock-id` marker and keeps using that store. Add more locks after the
first upgraded start. The old stored setup PIN is removed; identities and
credentials remain. Do not delete state to upgrade or restart.

## Implemented protocol surface

| Service / characteristic | Type | Behavior |
| --- | --- | --- |
| Accessory Information / Hardware Finish | 26C | Read-only Wallet finish TLV |
| Lock Mechanism | 45 | Simulated current/target lock state |
| Lock Management | 44 | Version; management commands return unsupported |
| NFC Access | 266 | Home Key provisioning service |
| Configuration State | 263 | Read/events, fixed 0 as in Python reference |
| NFC Access Control Point | 264 | Read/write/write-response; nested TLV8 |
| NFC Access Supported Configuration | 265 | Advertises 16 issuers and 16 credentials |

Reader-key GET/ADD/REMOVE and endpoint ADD are implemented. Endpoint GET and
REMOVE return Not Supported because their complete wire semantics are not
established by the reference. Entire issuer removal through HomeKit unpairing
is implemented. Per-device removal while keeping an issuer paired needs a
follow-up implementation before this controls a real lock. Active/inactive
credential state is enforced by NFC authentication.

NFC STANDARD authentication and the ESP32 BLE APDU bridge are implemented.
FAST, NFC attestation, a physical-lock adapter and a cloud
API are not implemented. Unknown/inactive endpoints are rejected.

## Build and test

```sh
make                 # bin/homekey
make test            # application tests + relevant patched HAP tests
make race            # same tests with Go race detection (requires C compiler)
make vet
make clean           # removes binaries; preserves credentials
```

The application builds without CGO. Linux amd64 was executed; macOS arm64 and
amd64 were cross-compiled. Actual LAN discovery and Wallet setup on your
Mac/iPhone remain to be verified.

Tests cover TLV fragmentation and malformed input, reader/device provisioning,
unknown issuers, invalid keys, duplicate writes, atomic persistence failure,
restart, unpair cleanup, concurrent access, characteristic metadata, and
rejection of unauthenticated HAP requests.

An independent `aiohomekit` client was also used during development to verify
SRP Pair Setup, Pair Verify, encrypted accessory discovery and provisioning,
repeated requests, reconnect after server restart, and unpair/revocation.
That client is a development test tool only and is not shipped or required.
It does not emulate Apple's Wallet issuance service.

## Reference

Protocol sources:
- https://github.com/kormax/apple-home-key-reader (commit 96754078121cfe1792a1e2c402873d105c8016e3)
- https://github.com/kupa22/apple-homekey
- https://github.com/ikalchev/HAP-python (Home Key characteristic metadata)

Apache-2.0; see LICENSE, NOTICE and the retained third-party license.

### Home Key ECP

Version 0.0.5 requires the matching Home Key ECP relay firmware. Go supplies the
public Home group ID over BLE on reconnect and after provisioning changes; the
ESP32 emits ECP before activating a card so iOS can route NFC to Home Key. The
phone's Express Mode setting governs whether user approval is required.
See [NFC deployment instructions](ha-app/NFC.md).
