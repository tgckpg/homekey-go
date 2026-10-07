package app

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"homekey.local/provisioner/internal/blegateway"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// HA ingress authenticates the user. Only its gateway may reach this listener;
// standalone mode binds to loopback instead.
func Handler(locks []*Lock, readers []ReaderConfig, ingress bool, settings ...*Settings) http.Handler {
	mux := http.NewServeMux()
	if len(settings) > 0 && settings[0] != nil {
		cfg := settings[0]
		physicalRoutes(mux, cfg)
		mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
			c, rev := cfg.Snapshot()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(struct {
				Config   Config `json:"config"`
				Revision string `json:"revision"`
			}{c, rev})
		})
		mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Homekey-Action") != "configuration" {
				http.Error(w, "missing action header", 403)
				return
			}
			var payload struct {
				Config   Config `json:"config"`
				Revision string `json:"revision"`
			}
			r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&payload); err != nil {
				http.Error(w, "invalid configuration JSON", 400)
				return
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				http.Error(w, "trailing configuration data", 400)
				return
			}
			if err := cfg.Save(payload.Config, payload.Revision); err != nil {
				code := 400
				if errors.Is(err, ErrConflict) || errors.Is(err, ErrReloading) {
					code = 409
				}
				http.Error(w, err.Error(), code)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			if cfg.Reload != nil {
				cfg.Reload()
			}
		})
		mux.HandleFunc("GET /api/discovery", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
			defer cancel()
			c, _ := cfg.Snapshot()
			known := make([]string, 0, len(c.Readers))
			for _, r := range c.Readers {
				known = append(known, r.Address)
			}
			d, err := blegateway.Discover(ctx, known)
			if err != nil {
				http.Error(w, err.Error(), 503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(d)
		})
	}
	mux.HandleFunc("GET /api/locks", func(w http.ResponseWriter, r *http.Request) {
		statuses := make([]LockStatus, 0, len(locks))
		for _, l := range locks {
			statuses = append(statuses, l.Status())
		}
		readerViews := make([]ReaderView, 0, len(readers))
		for _, reader := range readers {
			v := ReaderView{ReaderConfig: reader}
			if _, err := Group(Assigned(locks, reader.ID)); err != nil {
				v.Warning = err.Error()
			}
			readerViews = append(readerViews, v)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Locks   []LockStatus `json:"locks"`
			Readers []ReaderView `json:"readers"`
		}{statuses, readerViews})
	})
	mux.HandleFunc("POST /api/locks/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Homekey-Action") != "pairing" {
			http.Error(w, "missing action header", http.StatusForbidden)
			return
		}
		for _, l := range locks {
			if l.Config.ID == r.PathValue("id") {
				switch r.PathValue("action") {
				case "pair":
					if err := l.Pair(); err != nil {
						http.Error(w, err.Error(), http.StatusConflict)
						return
					}
				case "cancel":
					l.CancelPair()
				default:
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if ingress && (err != nil || host != "172.30.32.2") {
			http.Error(w, "ingress only", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); !ingress && origin != "" && !strings.HasSuffix(origin, "://"+r.Host) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'self'")
		mux.ServeHTTP(w, r)
	})
}

type ReaderView struct {
	ReaderConfig
	Warning string `json:"warning,omitempty"`
}

//go:embed page.html
var page string
