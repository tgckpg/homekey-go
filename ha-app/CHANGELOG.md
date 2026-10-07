## 0.0.10

- Better connection management and error messsages

## 0.0.9

- Fixed missing remove lock button

## 0.0.8

- Sesame: Prefers fresh advertisements on reconnect
- Sesame: Better error messages

## 0.0.7

- Move configuration into Open Web UI, importing existing HA options once.
- Discover compatible Home Key Bluetooth readers and local adapters.
- Select reader devices from populated dropdowns and lock assignments via checkboxes.
- Generate immutable IDs automatically; give readers editable friendly names.
- Trim configuration whitespace and normalize Bluetooth addresses.
- Save atomically, reject conflicting edits, and reload HomeKit/BLE connections.

## 0.0.6

- Configure lock and reader lists; remove single-lock startup options.
- Preserve independent HomeKit identities, pairings and credentials per lock.
- Share one BLE connection across assigned locks with per-lock authorization.
- Add HA ingress Web UI with a five-minute HomeKit pairing window.
- Keep setup codes in memory; hide after pairing and block setup outside a window.
- Preserve existing single-lock state through a one-time ID binding.

## 0.0.5
- Acknowledge removal of the placeholder returned by GET
- Supply the provisioned Home group identifier to matching ESP32 firmware over BLE.
- Refresh ECP configuration after provisioning changes and every reconnect.
- Enable Home Key ECP polling before NFC activation, including Express Mode routing.
- Requires matching homekey-relay ECP firmware; no new app configuration.

## 0.0.4
DEBUG added logs

## 0.0.3

- Relay NFC APDUs through the ESP32 BLE reader, keeping the card active.
- Authenticate enrolled Home Keys using SELECT, AUTH0 and STANDARD AUTH1.
- Verify response MACs and device signatures before unlocking the virtual lock.
- Recheck credentials against concurrent HomeKit revocation; save persistent keys.
- Preserve PING/PONG, PN532 startup wake prefixes and GPIO mapping.

## 0.0.2

- Optional direct BlueZ BLE PING/PONG probe with reconnect and matching sequence numbers.
- Enable host D-Bus access and add BLE reader/adapter options.

# Changelog

## 0.0.1

- Package the existing HomeKit lock and Home Key provisioning service for HA OS.
- Persist pairing identity, PIN, and credentials in /data/state.
- Configure accessory identity, HAP port, LAN interface, and Wallet finish.
