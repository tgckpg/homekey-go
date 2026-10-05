// SPDX-License-Identifier: Apache-2.0
// Adapted from kormax/apple-home-key-reader entity.py/service.py; see NOTICE.
package provision

import (
	"crypto/elliptic"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"time"
)

const (
	Success byte = iota
	OutOfResources
	Duplicate
	DoesNotExist
	NotSupported
)
const Capacity = 16

func validScalar(b []byte) bool {
	n := new(big.Int).SetBytes(b)
	return len(b) == 32 && n.Sign() > 0 && n.Cmp(elliptic.P256().Params().N) < 0
}
func endpointID(pub []byte) string                  { h := sha1.Sum(pub); return hex.EncodeToString(h[:6]) }
func statusResponse(outer, tag, status byte) []byte { return TLV(outer, TLV(tag, []byte{status})) }

// Handle validates and commits a control point operation before returning its
// response. Malformed input is a HAP error, semantic failures are TLV statuses.
func (s *Store) Handle(raw []byte) (response []byte, err error) {
	f, err := DecodeTLV(raw)
	if err != nil {
		return nil, err
	}
	op, err := one(f, 1)
	if err != nil {
		return nil, err
	}
	r, hasReader := f[6]
	e, hasEndpoint := f[4]
	if hasReader == hasEndpoint {
		return nil, fmt.Errorf("exactly one request object required")
	}
	body := r
	if hasEndpoint {
		body = e
	}
	inner, err := DecodeTLV(body)
	if err != nil {
		return nil, err
	}
	log.Printf("Provisioning request: op=%d reader=%t endpoint=%t",
		op, hasReader, hasEndpoint)
	err = s.update(func(d *Data) error {
		if hasReader {
			response, err = readerOperation(&d.HomeKey, op, inner)
		} else {
			response, err = endpointOperation(&d.HomeKey, op, inner)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func readerOperation(c *Credentials, op byte, f Fields) ([]byte, error) {
	status := func(v byte) ([]byte, error) { return statusResponse(7, 2, v), nil }
	switch op {
	case 1:
		// Match the working Python implementation before enrollment: hash a zero
		// scalar when no reader key exists. This is an identifier, not a usable key.
		key := make([]byte, 32)
		if c.ReaderPrivateKey != "" {
			key, _ = hex.DecodeString(c.ReaderPrivateKey)
		}
		id, _ := hex.DecodeString(keyID(key))
		return TLV(7, TLV(1, id)), nil
	case 2:
		kt, err := one(f, 1)
		if err != nil {
			return nil, err
		}
		if kt != 2 {
			return status(NotSupported)
		}
		if !validScalar(f[2]) || len(f[3]) != 8 {
			return nil, fmt.Errorf("invalid reader key or identifier")
		}
		key, id := hex.EncodeToString(f[2]), hex.EncodeToString(f[3])
		if key == c.ReaderPrivateKey && id == c.ReaderIdentifier {
			return status(Duplicate)
		}
		// A new reader identity invalidates previous FAST secrets.
		for iid, issuer := range c.Issuers {
			for eid, ep := range issuer.Endpoints {
				ep.PersistentKey = ""
				issuer.Endpoints[eid] = ep
			}
			c.Issuers[iid] = issuer
		}
		c.ReaderPrivateKey = key
		c.ReaderIdentifier = id
		return status(Success)
	case 3:
		if len(f[4]) != 8 {
			return nil, fmt.Errorf("invalid reader key identifier")
		}
		if c.ReaderPrivateKey == "" {
			// Acknowledge removal of the placeholder returned by GET
			if hex.EncodeToString(f[4]) == keyID(make([]byte, 32)) {
				return status(Success)
			}
			return status(DoesNotExist)
		}
		key, _ := hex.DecodeString(c.ReaderPrivateKey)
		if hex.EncodeToString(f[4]) != keyID(key) {
			return status(DoesNotExist)
		}
		c.ReaderPrivateKey = ""
		c.ReaderIdentifier = ""
		for id, issuer := range c.Issuers {
			issuer.Endpoints = map[string]Endpoint{}
			c.Issuers[id] = issuer
		}
		return status(Success)
	default:
		return status(NotSupported)
	}
}

func endpointOperation(c *Credentials, op byte, f Fields) ([]byte, error) {
	status := func(v byte) ([]byte, error) { return statusResponse(5, 3, v), nil }
	if op != 2 {
		return status(NotSupported)
	} // GET/REMOVE wire semantics not established in reference.
	kt, err := one(f, 1)
	if err != nil {
		return nil, err
	}
	if kt != 2 {
		return status(NotSupported)
	}
	if len(f[2]) != 64 || len(f[3]) != 8 {
		return nil, fmt.Errorf("invalid device credential length")
	}
	state := byte(1)
	if _, ok := f[4]; ok {
		state, err = one(f, 4)
		if err != nil || state > 1 {
			return nil, fmt.Errorf("invalid credential state")
		}
	}
	pub := append([]byte{4}, f[2]...)
	x, _ := elliptic.Unmarshal(elliptic.P256(), pub)
	if x == nil {
		return nil, fmt.Errorf("invalid P-256 public key")
	}
	iid := hex.EncodeToString(f[3])
	issuer, ok := c.Issuers[iid]
	if !ok {
		return status(DoesNotExist)
	}
	if c.ReaderPrivateKey == "" {
		return status(DoesNotExist)
	}
	eid := endpointID(pub)
	old, exists := issuer.Endpoints[eid]
	if exists && old.PublicKey != hex.EncodeToString(pub) {
		return status(NotSupported)
	}
	code := Success
	if exists && old.KeyState == state {
		code = Duplicate
	} else {
		total := 0
		for _, i := range c.Issuers {
			total += len(i.Endpoints)
		}
		if !exists && total >= Capacity {
			return status(OutOfResources)
		}
		if !exists {
			old = Endpoint{PublicKey: hex.EncodeToString(pub), KeyType: 2, EnrolledAt: time.Now().Unix()}
		}
		old.KeyState = state
		issuer.Endpoints[eid] = old
		c.Issuers[iid] = issuer
	}
	body := append(TLV(2, f[3]), TLV(3, []byte{code})...)
	return TLV(5, body), nil
}
