# Sesame OS3 driver

This directory contains the Sesame protocol, BlueZ transport and hardware-free
tests. Supported advertised models: Sesame 5, 5 Pro, 6 and 6 Pro. It does not
implement Sesame OS2, Hub/Matter setup, cloud guest-key signing or firmware updates.

The protocol is adapted from the MIT-licensed CANDY HOUSE Android SDK at
`d12f23c25bffd5adf6361abd0db82bb144441875`. See `LICENSE` and the root `NOTICE`.

## Integration

`Discover` scans powered local BlueZ adapters. Devices are matched by their
advertised Sesame UUID and supported product ID, not by a friendly name or fixed
Bluetooth MAC. `Register` enrolls an unregistered device with P-256 ECDH and calls
a persistence callback as soon as the 16-byte credential is available. It never
issues a reset. Registration here provisions BLE access, not a Sesame cloud
account. `Probe` validates an imported local key by logging in without moving
the motor. Cloud-only guest keys are not supported.

`Client.Run` maintains a connection and reconnects after transport or crypto
failure. `Action` serializes operations on that connection and waits for the
lock's result code. Failed/timed-out operations are not replayed after reconnect.
`State` returns the latest immutable snapshot; pointers in snapshots must not be
modified by callers. Each reconnect clears state until new publications arrive. Discovery prefers
advertisements received in the current scan when BlueZ has cached multiple
addresses for one UUID. Connection setup and login have separate deadlines;
errors identify discovery, connection, GATT services, notification subscription
or initial-token/login waits, and connection failures are logged without keys.

Commands: `lock`, `unlock`, `set-lock`, `set-unlock`, `set-boundary`. Calibration
captures the current reported position; saving either endpoint preserves the
other endpoint. Boundary capture is allowed only after firmware has published
its boundary capability. Positions use the SDK's signed 16-bit angle values;
negative values, including -1, are valid.

OS3 framing uses 19 payload bytes per GATT fragment. Local login uses AES-CMAC.
The fixed CCM profile is 13-byte nonce, 4-byte tag, AAD 00; the nonce contains an
8-byte LE counter, zero, and a 4-byte token. Unsupported token lengths fail
explicitly. No keys, authentication data or decrypted packets are logged.

`Link`/`Dialer` separate the session protocol from BlueZ so a future ESP32 GATT
tunnel can reuse this implementation. This release requires the HA/server's
local Bluetooth adapter to reach the lock; it does not use HA Bluetooth proxies
or add an ESP32 central role.

## Validation

Run `go test -race ./...` and `go vet ./...` at repository root. Tests cover RFC
4493 CMAC vectors, independently generated OpenSSL/Python CCM vectors with high
counter bytes, tampering, framing, P-256 registration, commands, calibration,
unsupported boundary settings, cancellation, private storage and HTTP validation.

Still requires real hardware validation: local BLE registration/login, initial
token length and response encryption, notifications during manual movement,
calibration/motor behavior, coexistence with the phone app, and battery impact
of a persistent connection. Unit tests do not establish those device behaviors.
