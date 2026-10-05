// SPDX-License-Identifier: Apache-2.0
package provision

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// AuthenticationCredentials returns an isolated snapshot; never log it.
func (s *Store) AuthenticationCredentials() Credentials {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.data.HomeKey)
	var c Credentials
	_ = json.Unmarshal(b, &c)
	return c
}
func authorized(current, expected Credentials, iid, eid, pub string) bool {
	if current.ReaderPrivateKey != expected.ReaderPrivateKey || current.ReaderIdentifier != expected.ReaderIdentifier {
		return false
	}
	issuer, ok := current.Issuers[iid]
	if !ok {
		return false
	}
	ep, ok := issuer.Endpoints[eid]
	return ok && ep.PublicKey == pub && ep.KeyState == 1 && ep.KeyType == 2
}
func (s *Store) SaveAuthenticatedKey(expected Credentials, iid, eid, pub string, key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("invalid persistent key")
	}
	return s.update(func(d *Data) error {
		if !authorized(d.HomeKey, expected, iid, eid, pub) {
			return fmt.Errorf("credentials revoked or changed during authentication")
		}
		issuer := d.HomeKey.Issuers[iid]
		ep := issuer.Endpoints[eid]
		ep.PersistentKey = hex.EncodeToString(key)
		issuer.Endpoints[eid] = ep
		d.HomeKey.Issuers[iid] = issuer
		return nil
	})
}

// WithAuthorization serializes the final unlock with HomeKit revocations.
// The callback must not call back into Store.
func (s *Store) WithAuthorization(expected Credentials, iid, eid, pub string, fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !authorized(s.data.HomeKey, expected, iid, eid, pub) {
		return fmt.Errorf("credentials revoked or changed before unlock")
	}
	return fn()
}

// ReaderGroupIdentifier is public ECP routing metadata derived from the
// provisioned reader key, matching the first half of the NFC reader ID.
// A nil result disables ECP while the Home has no provisioned reader key.
func (s *Store) ReaderGroupIdentifier() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	scalar, err := hex.DecodeString(s.data.HomeKey.ReaderPrivateKey)
	if err != nil || len(scalar) != 32 {
		return nil
	}
	group, _ := hex.DecodeString(keyID(scalar))
	return group
}
