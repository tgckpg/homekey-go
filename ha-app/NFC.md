# NFC STANDARD authentication

This release connects the HomeKit provisioning store to the ESP32 reader. Go
runs SELECT, AUTH0 and STANDARD AUTH1; the ESP32 keeps the PN532 target active
and relays APDUs. A verified, active enrolled endpoint unlocks the **virtual**
HomeKit lock and updates its target/current state to unlocked. There is no
physical actuator integration in this release.

## Deploy both sides

1. Apply the matching `homekey-relay` changes and build/flash its ESP32 project:

   ```sh
   cd esp32
   idf.py build flash monitor
   ```

   The PN532 UART remains TX GPIO3 / RX GPIO4 at 115200 baud. The firmware
   preserves the startup wake prefix on firmware and SAM commands and their
   existing initialization order. NFC runs in its own task with an 8192-byte
   stack; there is no need to edit an existing sdkconfig's main-task stack.

2. Build and publish your usual `homekey-go`, then stage the HA app metadata
   using the existing repository workflow. Update the installed app and restart
   it. Keep its existing persistent `/data/state`.

3. Keep the current app configuration:

   ```yaml
   ble_reader: "70:AF:09:16:42:5A"
   ble_adapter: hci0
   ```

   The HA app still needs host D-Bus and host networking. No new port is added.
   The HACS integration is not involved in this direct Go-to-ESP32 connection.

4. Hold the phone at the reader with its existing Home Key installed. ECP
   should select the key; Express Mode can authenticate without opening Wallet.
   If Express Mode is disabled, approve the presented Home Key. Remove it
   between attempts. Set the virtual lock to locked first if you want to see
   the successful transition in Home.

Expected Go milestones:

```text
BLE gateway: connected to ...; APDU write_size=...
BLE gateway: Home Key ECP configured reader=... enabled=true
NFC card reader=... session=... uid_len=... sak=0x20
Home Key SELECT accepted; protocol=2.0
Home Key AUTH0 key exchange complete
Home Key authenticated reader=... session=... endpoint=...; virtual lock unlocked
```

An ordinary card can be detected without having the Home Key applet. SELECT
status `6a82` means the applet was unavailable for that attempt. It does not
mean that UID detection or BLE is broken. `AUTH1: unknown, inactive or invalid
endpoint` does not grant access; check that the phone has an enrolled key.

If the app reports missing relay characteristics after flashing, BlueZ may
still have the old GATT services cached. Disconnect/reconnect first. If they
remain missing, remove **only this reader's** BlueZ device entry on the host
and let the gateway rediscover it. There is no need to remove the HomeKit lock
or its provisioned keys.

## Scope

- STANDARD authentication for active endpoints enrolled through HomeKit.
- Fresh P-256 ephemeral keys and a nonce for each attempt.
- Reader proof, secure response MAC, endpoint signature verification.
- Persistent key saved only after successful verification.
- Current credentials rechecked before persistence and again under the store
  lock while updating the virtual lock, preventing concurrent revocation from
  using an old snapshot.
- PING/PONG remains available; its polling pauses during authentication.
- No credential payloads, private/session keys or APDU contents in normal logs.

Home Key ECP routing is implemented in the matching firmware. It requires both
updates: Go supplies the public Home group ID, and the ESP32 broadcasts it before
card activation. No private key is sent over BLE. Firmware waits with RF off
until configured and clears the configuration on reconnect. Go updates the
group when provisioning changes. ECP itself does not authenticate a device.

FAST authentication and NFC attestation for previously unknown/shared devices
are not implemented yet. The stored persistent key is preparation for FAST; this release still
performs STANDARD on every tap.

The NFC session expires after 15 seconds. Go allows 12 seconds for authentication,
5 seconds per APDU and 2 seconds for completion UX. APDUs are bounded to 240
bytes; extended/chained PN532 APDUs and ISO18013 attestation transfers are not
supported. An error, removal, timeout or disconnect never authenticates a key.

## Verification

Go tests cover an end-to-end simulated phone, response tampering, unknown and
inactive endpoints, revocation and reader changes, BER-TLV failures and stale
BLE responses. Independent Python `cryptography` vectors verify X9.63/HKDF and
secure-response decryption; RFC 4493 vectors verify AES-CMAC.

The firmware host tests exercise UART frame parsing and the BLE mailbox with
AddressSanitizer/UndefinedBehaviorSanitizer:

```sh
./tests/run.sh
```

These host tests do not replace an ESP-IDF build or an actual phone tap. This
patch was not tested on ESP32/PN532 hardware.
