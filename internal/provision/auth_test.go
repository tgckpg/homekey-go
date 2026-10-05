package provision

import (
	"bytes"
	"testing"
)

func TestAuthenticationPersistenceAndRevocation(t *testing.T) {
	s, dir, iid := paired(t)
	if _, e := s.Handle(readerAdd()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Handle(endpointAdd(iid)); e != nil {
		t.Fatal(e)
	}
	c := s.AuthenticationCredentials()
	var eid, pub string
	for id, ep := range c.Issuers[iid].Endpoints {
		eid = id
		pub = ep.PublicKey
	}
	key := bytes.Repeat([]byte{7}, 32)
	if e := s.SaveAuthenticatedKey(c, iid, eid, pub, key); e != nil {
		t.Fatal(e)
	}
	restored, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if restored.AuthenticationCredentials().Issuers[iid].Endpoints[eid].PersistentKey == "" {
		t.Fatal("persistent key lost")
	}
	// Mutating a snapshot must never modify the live credential store.
	delete(c.Issuers, iid)
	c = s.AuthenticationCredentials()
	calls := 0
	if e := s.WithAuthorization(c, iid, eid, pub, func() error { calls++; return nil }); e != nil {
		t.Fatal(e)
	}
	if e := s.Delete("controller.pairing"); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveAuthenticatedKey(c, iid, eid, pub, key); e == nil {
		t.Fatal("revoked credential persisted")
	}
	if e := s.WithAuthorization(c, iid, eid, pub, func() error { calls++; return nil }); e == nil {
		t.Fatal("revoked credential unlocked")
	}
	if calls != 1 {
		t.Fatal("callback ran after revocation")
	}
}
func TestAuthenticationRejectsReaderChangeAndInactive(t *testing.T) {
	for _, mode := range []string{"reader", "inactive"} {
		t.Run(mode, func(t *testing.T) {
			s, _, iid := paired(t)
			s.Handle(readerAdd())
			s.Handle(endpointAdd(iid))
			c := s.AuthenticationCredentials()
			var eid, pub string
			for id, ep := range c.Issuers[iid].Endpoints {
				eid = id
				pub = ep.PublicKey
			}
			e := s.update(func(d *Data) error {
				if mode == "reader" {
					d.HomeKey.ReaderIdentifier = "0908070605040302"
				} else {
					i := d.HomeKey.Issuers[iid]
					ep := i.Endpoints[eid]
					ep.KeyState = 0
					i.Endpoints[eid] = ep
					d.HomeKey.Issuers[iid] = i
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			if e = s.WithAuthorization(c, iid, eid, pub, func() error { t.Fatal("unauthorized callback"); return nil }); e == nil {
				t.Fatal("changed credentials accepted")
			}
		})
	}
}
