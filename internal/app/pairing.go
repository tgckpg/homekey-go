package app

import (
	"crypto/rand"
	"fmt"
	"github.com/brutella/hap"
	"github.com/go-chi/chi"
	"homekey.local/provisioner/internal/hkserver"
	"homekey.local/provisioner/internal/provision"
	"math/big"
	"net/http"
	"sync"
	"time"
)

type Lock struct {
	Config  LockConfig
	Store   *provision.Store
	Device  *hkserver.Device
	pairing sync.Mutex
	until   time.Time
	pin     string
}

func NewLock(c LockConfig, store *provision.Store, ifaces []string) (*Lock, error) {
	// PIN exists only in process memory. It is never printed or persisted.
	var pin string
	for {
		n, err := rand.Int(rand.Reader, big.NewInt(100000000))
		if err != nil {
			return nil, err
		}
		pin = fmt.Sprintf("%08d", n)
		if !hap.InvalidPins[pin] {
			break
		}
	}
	d, err := hkserver.New(store, hkserver.Config{Name: c.Name, Serial: c.ID, Finish: c.Finish, PIN: pin, Address: fmt.Sprintf(":%d", c.Port), Interfaces: ifaces})
	if err != nil {
		return nil, err
	}
	l := &Lock{Config: c, Store: store, Device: d, pin: pin}
	mux := d.Server.ServeMux().(*chi.Mux)
	var original http.Handler
	var find func(chi.Routes)
	find = func(r chi.Routes) {
		for _, route := range r.Routes() {
			if route.Pattern == "/pair-setup" {
				original = route.Handlers["POST"]
			}
			if route.SubRoutes != nil {
				find(route.SubRoutes)
			}
		}
	}
	find(mux)
	if original == nil {
		return nil, fmt.Errorf("HAP pair-setup route missing")
	}
	mux.Method("POST", "/pair-setup", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.pairing.Lock()
		defer l.pairing.Unlock()
		if !time.Now().Before(l.until) || store.Summary().PairedControllers > 0 {
			w.Header().Set("Content-Type", hap.HTTPContentTypePairingTLV8)
			w.Write([]byte{6, 1, 2, 7, 1, 6}) // M2 / unavailable
			return
		}
		original.ServeHTTP(w, r)
		if store.Summary().PairedControllers > 0 {
			l.until = time.Time{}
		}
	}))
	return l, nil
}
func (l *Lock) Pair() error {
	l.pairing.Lock()
	defer l.pairing.Unlock()
	if l.Store.Summary().PairedControllers > 0 {
		return fmt.Errorf("lock is already paired")
	}
	if !time.Now().Before(l.until) {
		l.until = time.Now().Add(5 * time.Minute)
	}
	return nil
}
func (l *Lock) CancelPair() { l.pairing.Lock(); defer l.pairing.Unlock(); l.until = time.Time{} }

type LockStatus struct {
	LockConfig
	Summary provision.Summary `json:"summary"`
	PIN     string            `json:"pin,omitempty"`
	Until   time.Time         `json:"until,omitempty"`
}

func (l *Lock) Status() LockStatus {
	l.pairing.Lock()
	defer l.pairing.Unlock()
	s := LockStatus{LockConfig: l.Config, Summary: l.Store.Summary()}
	if s.Summary.PairedControllers == 0 && time.Now().Before(l.until) {
		// Follow the format shown in home app XXXX-XXXX
		s.PIN = l.pin[:4] + "-" + l.pin[4:]
		s.Until = l.until
	}
	return s
}
