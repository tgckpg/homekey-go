package app

import (
	"bytes"
	"context"
	"fmt"
	"homekey.local/provisioner/internal/blegateway"
	"homekey.local/provisioner/internal/nfcauth"
	"homekey.local/provisioner/internal/provision"
	"log"
	"time"
)

func Assigned(locks []*Lock, id string) []*Lock {
	var out []*Lock
	for _, l := range locks {
		for _, r := range l.Config.Readers {
			if r == id {
				out = append(out, l)
				break
			}
		}
	}
	return out
}
func Group(locks []*Lock) ([]byte, error) {
	var group []byte
	for _, l := range locks {
		g := l.Store.ReaderGroupIdentifier()
		if len(g) == 0 {
			continue
		}
		if group != nil && !bytes.Equal(group, g) {
			return nil, fmt.Errorf("shared reader locks belong to different Home Key groups; use separate readers")
		}
		group = g
	}
	return group, nil
}
func Authenticate(ctx, serviceCtx context.Context, locks []*Lock, reader string, card blegateway.Card, relay *blegateway.Relay) (err error) {
	if card.SAK&0x20 == 0 {
		return fmt.Errorf("card does not support ISO-DEP")
	}
	success := false
	defer func() {
		finishCtx, done := context.WithTimeout(serviceCtx, 2*time.Second)
		defer done()
		if e := nfcauth.Finish(finishCtx, relay, success); e != nil {
			log.Printf("NFC control flow: %v", e)
		}
	}()
	if _, err = Group(locks); err != nil {
		return err
	}
	// All locks in one Apple Home share the reader group key. Use one enrolled
	// identity for this transaction; recheck each lock's own endpoint enrollment.
	var source *Lock
	for _, l := range locks {
		if s := l.Store.Summary(); s.ReaderProvisioned && s.Endpoints > 0 {
			source = l
			break
		}
	}
	if source == nil {
		return fmt.Errorf("no assigned lock has enrolled Home Key credentials")
	}
	snapshots := make(map[*Lock]provision.Credentials)
	credentials := source.Store.AuthenticationCredentials()
	credentials.Issuers = make(map[string]provision.Issuer)
	for _, l := range locks {
		snapshot := l.Store.AuthenticationCredentials()
		snapshots[l] = snapshot
		if snapshot.ReaderPrivateKey != credentials.ReaderPrivateKey {
			continue
		}
		for iid, issuer := range snapshot.Issuers {
			merged, ok := credentials.Issuers[iid]
			if !ok {
				merged = provision.Issuer{PublicKey: issuer.PublicKey, Endpoints: map[string]provision.Endpoint{}}
			}
			for eid, endpoint := range issuer.Endpoints {
				if endpoint.KeyState != 1 {
					continue
				}
				if existing, ok := merged.Endpoints[eid]; ok && existing.PublicKey != endpoint.PublicKey {
					return fmt.Errorf("conflicting endpoint enrollment")
				}
				merged.Endpoints[eid] = endpoint
			}
			credentials.Issuers[iid] = merged
		}
	}
	result, err := nfcauth.Authenticate(ctx, relay, credentials)
	if err != nil {
		return err
	}
	// Persistent keys depend on the identity used in AUTH0. Only cache on
	// that identity's lock, and only if it enrolled the authenticated endpoint.
	sourceSnapshot := snapshots[source]
	if issuer, ok := sourceSnapshot.Issuers[result.IssuerID]; ok {
		if ep, ok := issuer.Endpoints[result.EndpointID]; ok && ep.KeyState == 1 {
			if err = source.Store.SaveAuthenticatedKey(sourceSnapshot, result.IssuerID, result.EndpointID, result.PublicKey, result.PersistentKey); err != nil {
				return err
			}
		}
	}
	success = unlockAuthorized(ctx, locks, snapshots, credentials.ReaderPrivateKey, result, reader, card.Session) > 0
	if !success {
		return fmt.Errorf("no assigned lock authorizes this endpoint")
	}
	return nil
}

func unlockAuthorized(ctx context.Context, locks []*Lock, snapshots map[*Lock]provision.Credentials, groupKey string, result *nfcauth.Result, reader string, session uint32) int {
	unlocked := 0
	for _, l := range locks {
		expected := snapshots[l]
		if expected.ReaderPrivateKey != groupKey {
			continue
		}
		e := l.Store.WithAuthorization(expected, result.IssuerID, result.EndpointID, result.PublicKey, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := l.Device.Lock.LockTargetState.SetValue(0); err != nil {
				return err
			}
			return l.Device.Lock.LockCurrentState.SetValue(0)
		})
		if e != nil {
			continue
		}
		unlocked++
		log.Printf("Home Key authenticated reader=%s session=%d endpoint=%s lock=%s; virtual lock unlocked", reader, session, result.EndpointID, l.Config.ID)
	}
	return unlocked
}
