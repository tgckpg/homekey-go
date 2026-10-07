package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"homekey.local/provisioner/drivers/sesame"
	"homekey.local/provisioner/internal/physical"
)

// BindPhysical installs command handlers before HAP starts. The returned worker
// projects confirmed actuator state, including manual movement, into HAP.
func BindPhysical(locks []*Lock, m *physical.Manager) func(context.Context) {
	for _, l := range locks {
		l.Physical = m
		if len(l.Config.PhysicalLocks) == 0 {
			continue
		}
		l.Device.Lock.LockCurrentState.SetValue(3) // Unknown until live status arrives.
		l.Device.Lock.LockTargetState.OnSetRemoteValue(func(v int) error {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			return <-m.Dispatch(ctx, l.Config.PhysicalLocks, v == 1)
		})
	}
	return func(ctx context.Context) {
		timer := time.NewTicker(500 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				states := map[string]sesame.State{}
				for _, v := range m.Views() {
					states[v.ID] = v.State
				}
				for _, l := range locks {
					if len(l.Config.PhysicalLocks) == 0 {
						continue
					}
					state := aggregatePhysical(l.Config.PhysicalLocks, states)
					previous := l.Device.Lock.LockCurrentState.Value()
					l.Device.Lock.LockCurrentState.SetValue(state)
					// Manual turns must also settle HomeKit's target. Do not
					// overwrite an outstanding target on unchanged old status.
					if state < 2 && state != previous {
						l.Device.Lock.LockTargetState.SetValue(state)
					}
				}
			}
		}
	}
}
func aggregatePhysical(ids []string, states map[string]sesame.State) int {
	current := -1
	for _, id := range ids {
		s, ok := states[id]
		if !ok || !s.Online || s.Position == nil || !s.Stopped || s.Critical {
			return 3
		}
		v := 0
		if s.Locked {
			v = 1
		}
		if current != -1 && current != v {
			return 3
		}
		current = v
	}
	if current < 0 {
		return 3
	}
	return current
}
func physicalRoutes(mux *http.ServeMux, cfg *Settings) {
	if cfg.Physical == nil {
		return
	}
	mux.HandleFunc("GET /api/physical-locks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg.Physical.Views())
	})
	mux.HandleFunc("GET /api/physical-locks/discovery", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		d, e := sesame.Discover(ctx)
		if e != nil {
			http.Error(w, e.Error(), 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(d)
	})
	mux.HandleFunc("DELETE /api/physical-locks/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Homekey-Action") != "physical-lock" {
			http.Error(w, "missing action header", 403)
			return
		}
		if e := cfg.RemovePhysical(r.PathValue("id")); e != nil {
			code := http.StatusConflict
			if errors.Is(e, physical.ErrNotFound) {
				code = http.StatusNotFound
			}
			http.Error(w, e.Error(), code)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/physical-locks", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Homekey-Action") != "physical-lock" {
			http.Error(w, "missing action header", 403)
			return
		}
		var payload physical.Enrollment
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if e := d.Decode(&payload); e != nil {
			http.Error(w, "invalid physical lock JSON", 400)
			return
		}
		var extra any
		if e := d.Decode(&extra); e != io.EOF {
			http.Error(w, "trailing data", 400)
			return
		}
		record, e := cfg.Physical.Add(r.Context(), payload)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(record)
	})
	mux.HandleFunc("POST /api/physical-locks/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Homekey-Action") != "physical-lock" {
			http.Error(w, "missing action header", 403)
			return
		}
		action := r.PathValue("action")
		switch action {
		case "lock", "unlock", "set-lock", "set-unlock", "set-boundary":
		default:
			http.NotFound(w, r)
			return
		}
		if !cfg.Physical.Has(r.PathValue("id")) {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if e := cfg.Physical.Action(ctx, r.PathValue("id"), action); e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
