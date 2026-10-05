# Home Key in Go

Basically [kormax/apple-home-key-reader](https://github.com/kormax/apple-home-key-reader) in go.

## Quick start on macOS

```sh
brew install go
make
./bin/homekey -name "Test Lock"
```

Go 1.23 or newer is required. The first build downloads the pinned Go modules.
The executable prints a generated pairing code, for example `XXXX-XXXX`.
It saves that code and its accessory identity for later runs.

1. Put your iPhone and the computer running this program on the same LAN.
2. Open **Home → + → Add Accessory → More Options** (wording can vary by iOS).
3. Select **Penguin Door** and enter the printed pairing code.
4. Accept the uncertified-accessory prompt if displayed and complete setup.
5. Look for the Home Key setup/Express Mode flow and the resulting key in Wallet.

Use a compatible iPhone with a passcode and Apple Home/iCloud configured.
If setup creates only a normal lock, check the provisioning log and status
below; pairing alone does not prove that Wallet provisioned a key.

This accessory simulates locked/unlocked state for the Home app. A matching
ESP32/PN532 BLE reader can authenticate an enrolled Home Key and unlock this
virtual state. Changing it **does not operate a physical lock**. See
[the NFC guide](ha-app/NFC.md) for deployment and first-tap testing.

## Linux

Install Go 1.23+ through your usual package manager or the Go distribution:

```sh
make
./bin/homekey -name "Penguin Door" -interface eth0
```

No Avahi daemon is required; the process advertises mDNS itself. For the first
iPhone test, run directly on a machine on the phone's LAN. Containers and
Kubernetes need additional multicast/network configuration; a published TCP
port alone does not provide HomeKit discovery. Linux host networking is a
possible container setup; host networking on Docker Desktop is not equivalent
to a native macOS process for discovery.

## Options

```text
-name       Name in Home (default: Go Home Key)
-serial     Stable serial (default: GO-HOMEKEY-001)
-state      Persistent directory (default: ./state)
-pin        Eight-digit PIN, optionally with hyphens; generated if omitted
-addr       HAP TCP listen address (default: :51826)
-interface  LAN interface for mDNS; empty uses eligible interfaces
-finish     silver, black, gold, or tan (default: silver)
-status     Print credential counts and exit; safe while the server is running
```

The key artwork color may be determined by the first Home Key lock in the
home, so changing `-finish` later may not change the existing Wallet artwork.

On macOS an explicit interface might be `en0` or `en1`:

```sh
./bin/homekey -name "Penguin Door" -interface en0 -state ./state
```

Choose your actual LAN interface. Allow the executable through the firewall:
TCP 51826 (or the port you select), and local mDNS UDP 5353. Keep this service
on the LAN; HomeKit protocol traffic should not be put behind an HTTP reverse
proxy or published as an Internet service. Discovery usually does not cross
VLANs without an mDNS reflector and suitable routing.

## What successful provisioning looks like

Logs contain summaries such as:

```text
Provisioning exchange complete: reader=true issuers=1 endpoints=1
```

You can inspect the current counts without displaying any keys:

```sh
./bin/homekey -status -state ./state
```

Example:

```json
{"paired_controllers":1,"reader_provisioned":true,"issuers":1,"endpoints":1}
```

- `paired_controllers > 0`: HomeKit pairing completed.
- `reader_provisioned = true`: the controller supplied the reader identity/key.
- `endpoints > 0`: at least one phone/watch public credential was enrolled.
- The actual Home Key appearing in Wallet is the final confirmation on iOS.

## Persistence and lifecycle

`state/state.json` contains **both HAP pairing secrets and Home Key credentials**.
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
printed setup PIN is intentionally visible in the startup output. Do not
turn on the HAP library's raw debug logging when using real credentials.

To start over, first remove the accessory in Apple Home, stop the process,
then **move the complete state directory to a backup location** and restart.
That creates a new accessory identity. Do not delete state merely to restart.

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
FAST, ECP express wakeup, NFC attestation, a physical-lock adapter and a cloud
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
