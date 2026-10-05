// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"homekey.local/provisioner/internal/app"
	"homekey.local/provisioner/internal/blegateway"
	"homekey.local/provisioner/internal/provision"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	state := flag.String("state", "./state", "persistent state directory")
	config := flag.String("config", "./config.json", "lock and reader configuration JSON")
	webAddr := flag.String("web-addr", "127.0.0.1:8099", "configuration/pairing web address")
	ingress := flag.Bool("ingress", false, "restrict Web UI to the HA ingress gateway")
	status := flag.Bool("status", false, "print credential counts per lock and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional argument")
	}
	c, err := app.Load(*config)
	if err != nil {
		return err
	}
	var ifaces []string
	if c.Interface != "" {
		if _, err := net.InterfaceByName(c.Interface); err != nil {
			return err
		}
		ifaces = []string{c.Interface}
	}
	if err = os.MkdirAll(*state, 0700); err != nil {
		return err
	}
	if err = os.Chmod(*state, 0700); err != nil {
		return err
	}
	if !*status {
		unlock, e := lockState(filepath.Join(*state, ".lock"))
		if e != nil {
			return e
		}
		defer unlock()
	}
	var locks []*app.Lock
	summaries := map[string]provision.Summary{}
	for _, cfg := range c.Locks {
		dir := filepath.Join(*state, "locks", cfg.ID)
		store, e := provision.Open(dir)
		if e != nil {
			return e
		}
		if *status {
			summaries[cfg.ID] = store.Summary()
			continue
		}
		// Retire the old persistent setup code; HAP identities and pairings stay.
		if e = store.Delete("provisioning-pin"); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		l, e := app.NewLock(cfg, store, ifaces)
		if e != nil {
			return e
		}
		locks = append(locks, l)
	}
	if *status {
		return json.NewEncoder(os.Stdout).Encode(summaries)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var wg sync.WaitGroup
	failures := make(chan error, len(locks)+1)
	for _, l := range locks {
		wg.Add(1)
		go func(l *app.Lock) {
			defer wg.Done()
			if err := l.Device.Server.ListenAndServe(ctx); err != nil && ctx.Err() == nil {
				failures <- fmt.Errorf("lock %s: %w", l.Config.ID, err)
				cancel()
			}
		}(l)
	}
	for _, r := range c.Readers {
		assigned := app.Assigned(locks, r.ID)
		if len(assigned) == 0 {
			continue
		}
		wg.Add(1)
		go func(r app.ReaderConfig, assigned []*app.Lock) {
			defer wg.Done()
			lastGroupError := ""
			blegateway.Run(ctx, r.Address, r.Adapter, 10*time.Second, func(authCtx context.Context, card blegateway.Card, relay *blegateway.Relay) error {
				return app.Authenticate(authCtx, ctx, assigned, r.ID, card, relay)
			}, func() []byte {
				group, err := app.Group(assigned)
				if err != nil {
					if lastGroupError != err.Error() {
						log.Print(err)
						lastGroupError = err.Error()
					}
					return nil
				}
				lastGroupError = ""
				return group
			})
		}(r, assigned)
	}
	web := &http.Server{Addr: *webAddr, Handler: app.Handler(locks, c.Readers, *ingress), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := web.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failures <- err
			cancel()
		}
	}()
	log.Printf("Configured %d locks and %d readers; pairing UI at %s", len(locks), len(c.Readers), *webAddr)
	<-ctx.Done()
	shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	_ = web.Shutdown(shutdown)
	wg.Wait()
	select {
	case err := <-failures:
		return err
	default:
		return nil
	}
}
