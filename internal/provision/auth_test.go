package provision

import (
	"bytes"
	"encoding/hex"
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

func TestReaderGroupIdentifier(t *testing.T) {
	s, _, _ := paired(t)
	if s.ReaderGroupIdentifier() != nil {
		t.Fatal("unprovisioned reader has ECP group")
	}
	if _, err := s.Handle(readerAdd()); err != nil {
		t.Fatal(err)
	}
	// Independent SHA256("key-identifier" || scalar=1) vector.
	want := mustHex("539a3f91ed603fef")
	group := s.ReaderGroupIdentifier()
	if !bytes.Equal(group, want) {
		t.Fatalf("wrong group: %s", hex.EncodeToString(group))
	}
	group[0] ^= 1
	if !bytes.Equal(s.ReaderGroupIdentifier(), want) {
		t.Fatal("group snapshot aliases store")
	}
	if _, err := s.Handle(request(3, 6, TLV(4, want))); err != nil {
		t.Fatal(err)
	}
	if s.ReaderGroupIdentifier() != nil {
		t.Fatal("removed reader still has ECP group")
	}
}
