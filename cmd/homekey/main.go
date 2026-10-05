// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"homekey.local/provisioner/internal/blegateway"
	"homekey.local/provisioner/internal/nfcauth"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/brutella/hap"
	"homekey.local/provisioner/internal/hkserver"
	"homekey.local/provisioner/internal/provision"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	name := flag.String("name", "Go Home Key", "name shown in Apple Home")
	serial := flag.String("serial", "GO-HOMEKEY-001", "stable accessory serial number")
	state := flag.String("state", "./state", "persistent state directory; keep across restarts")
	pin := flag.String("pin", "", "8-digit pairing PIN (generated and saved if omitted)")
	addr := flag.String("addr", ":51826", "HAP listen address")
	iface := flag.String("interface", "", "LAN interface for mDNS; empty selects eligible interfaces")
	finish := flag.String("finish", "silver", "Wallet artwork: silver, black, gold, tan")
	status := flag.Bool("status", false, "print credential counts without starting the server")
	bleReader := flag.String("ble-reader", "", "ESP32 Bluetooth MAC; empty disables NFC gateway")
	bleAdapter := flag.String("ble-adapter", "hci0", "BlueZ adapter name")
	bleInterval := flag.Duration("ble-ping-interval", 10*time.Second, "BLE PING interval")
	flag.Parse()
	if *bleReader != "" {
		if err := blegateway.ValidateAddress(*bleReader); err != nil {
			return err
		}
		if !strings.HasPrefix(*bleAdapter, "hci") || strings.ContainsAny(*bleAdapter, "/. ") {
			return fmt.Errorf("invalid BLE adapter")
		}
		if *bleInterval < time.Second {
			return fmt.Errorf("BLE PING interval must be at least 1s")
		}
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional argument")
	}
	if err := os.MkdirAll(*state, 0700); err != nil {
		return err
	}
	if err := os.Chmod(*state, 0700); err != nil {
		return err
	}
	// Atomic state replacement permits a read-only summary while the server runs.
	if *status {
		store, err := provision.Open(*state)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(store.Summary())
	}
	unlock, err := lockState(filepath.Join(*state, ".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	store, err := provision.Open(*state)
	if err != nil {
		return err
	}
	if _, _, err := net.SplitHostPort(*addr); err != nil {
		return fmt.Errorf("invalid -addr: %w", err)
	}
	var ifaces []string
	if *iface != "" {
		if _, err := net.InterfaceByName(*iface); err != nil {
			return err
		}
		ifaces = []string{*iface}
	}
	p := strings.ReplaceAll(*pin, "-", "")
	if p == "" {
		v, e := store.Get("provisioning-pin")
		if e == nil {
			p = string(v)
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if p == "" {
		for {
			n, e := rand.Int(rand.Reader, big.NewInt(100000000))
			if e != nil {
				return e
			}
			p = fmt.Sprintf("%08d", n)
			if !hap.InvalidPins[p] {
				break
			}
		}
	}
	if len(p) != 8 || hap.InvalidPins[p] {
		return fmt.Errorf("PIN must be 8 digits and not a prohibited pattern")
	}
	for _, r := range p {
		if r < '0' || r > '9' {
			return fmt.Errorf("PIN must contain only digits")
		}
	}
	if err := store.Set("provisioning-pin", []byte(p)); err != nil {
		return err
	}
	dev, err := hkserver.New(store, hkserver.Config{Name: *name, Serial: *serial, Finish: *finish, PIN: p, Address: *addr, Interfaces: ifaces})
	if err != nil {
		return err
	}
	fmt.Printf("%s — virtual HomeKit lock\n", *name)
	fmt.Printf("\"Home\" app from Apple > Add Accessory > More Options > %s\n", *name)
	fmt.Printf("Pairing code: %s-%s\n", p[:4], p[4:])
	fmt.Println("NFC authentication enabled when a BLE reader is configured; virtual lock only.")
	log.Printf("HAP address %s; persistent state %s", *addr, *state)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var bleDone chan struct{}
	if *bleReader != "" {
		bleDone = make(chan struct{})
		go func() {
			defer close(bleDone)
			blegateway.Run(ctx, *bleReader, *bleAdapter, *bleInterval, func(authCtx context.Context, card blegateway.Card, relay *blegateway.Relay) (err error) {
				log.Printf("NFC card reader=%s session=%d uid_len=%d sak=0x%02x", *bleReader, card.Session, len(card.UID), card.SAK)
				if card.SAK&0x20 == 0 {
					return fmt.Errorf("card does not support ISO-DEP")
				}
				success := false
				defer func() {
					// Best-effort failure UX even if the authentication deadline expired.
					finishCtx, done := context.WithTimeout(ctx, 2*time.Second)
					defer done()
					if e := nfcauth.Finish(finishCtx, relay, success); e != nil {
						log.Printf("NFC control flow: %v", e)
					}
				}()
				credentials := store.AuthenticationCredentials()
				result, err := nfcauth.Authenticate(authCtx, relay, credentials)
				if err != nil {
					return err
				}
				if err = store.SaveAuthenticatedKey(credentials, result.IssuerID, result.EndpointID, result.PublicKey, result.PersistentKey); err != nil {
					return err
				}
				if authCtx.Err() != nil {
					return authCtx.Err()
				}
				err = store.WithAuthorization(credentials, result.IssuerID, result.EndpointID, result.PublicKey, func() error {
					if authCtx.Err() != nil {
						return authCtx.Err()
					}
					if e := dev.Lock.LockTargetState.SetValue(0); e != nil {
						return e
					}
					return dev.Lock.LockCurrentState.SetValue(0)
				})
				if err != nil {
					return err
				}
				success = true
				log.Printf("Home Key authenticated reader=%s session=%d endpoint=%s; virtual lock unlocked", *bleReader, card.Session, result.EndpointID)
				return nil
			}, store.ReaderGroupIdentifier)
		}()
	}
	err = dev.Server.ListenAndServe(ctx)
	stopped := ctx.Err() != nil
	cancel()
	if bleDone != nil {
		<-bleDone
	}
	if stopped || errors.Is(err, context.Canceled) || errors.Is(err, http.ErrServerClosed) || err == nil {
		return nil
	}
	return err
}
