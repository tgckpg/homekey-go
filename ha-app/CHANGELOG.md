## 0.0.5
Acknowledge removal of the placeholder returned by GET

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
