// SPDX-License-Identifier: Apache-2.0
package sesame

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

type session struct {
	link   Link
	rx     assembler
	cipher *sessionCipher
	state  State
	update func(State)
}

func (s *session) publish() {
	if s.update != nil {
		s.update(s.state)
	}
}
func (s *session) decode(fragment []byte) ([]byte, error) {
	kind, b, e := s.rx.feed(fragment)
	if e != nil || kind == 0 {
		return nil, e
	}
	if kind == 2 {
		if s.cipher == nil {
			return nil, errors.New("encrypted message before Sesame session")
		}
		return s.cipher.decrypt(b)
	}
	if s.cipher != nil {
		return nil, errors.New("plaintext message in authenticated Sesame session")
	}
	return b, nil
}
func (s *session) next(ctx context.Context) ([]byte, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case e := <-s.link.Done():
			if e == nil {
				e = errors.New("Sesame connection closed")
			}
			return nil, e
		case p, ok := <-s.link.Packets():
			if !ok {
				return nil, errors.New("Sesame notifications closed")
			}
			b, e := s.decode(p)
			if e != nil {
				return nil, e
			}
			if b != nil {
				return b, nil
			}
		}
	}
}
func (s *session) handle(b []byte) error {
	if len(b) < 2 {
		return errors.New("short Sesame notification")
	}
	if b[0] != 8 {
		return errors.New("unexpected Sesame response")
	}
	if b[1] == 14 {
		return errors.New("Sesame session restarted")
	}
	if e := s.state.publish(b[1], b[2:]); e != nil {
		return e
	}
	s.publish()
	return nil
}
func (s *session) initial(ctx context.Context) ([]byte, error) {
	b, e := s.next(ctx)
	if e != nil {
		return nil, e
	}
	if len(b) != 6 || b[0] != 8 || b[1] != 14 {
		return nil, errors.New("expected four-byte Sesame initial token")
	}
	return b[2:], nil
}
func (s *session) exchange(ctx context.Context, item byte, payload []byte, encrypted bool) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	b := append([]byte{item}, payload...)
	kind := byte(1)
	if encrypted {
		if s.cipher == nil {
			return nil, errors.New("Sesame not authenticated")
		}
		var e error
		b, e = s.cipher.encrypt(b)
		if e != nil {
			return nil, e
		}
		kind = 2
	}
	fs, e := fragments(kind, b)
	if e != nil {
		return nil, e
	}
	for _, f := range fs {
		if e = s.link.Write(ctx, f); e != nil {
			return nil, e
		}
	}
	for {
		b, e := s.next(ctx)
		if e != nil {
			return nil, e
		}
		if len(b) < 2 {
			return nil, errors.New("short Sesame response")
		}
		if b[0] == 7 {
			if len(b) < 3 || b[1] != item {
				return nil, errors.New("unexpected Sesame response item")
			}
			if b[2] != 0 {
				return nil, &ResultError{item, b[2]}
			}
			return b[3:], nil
		}
		if e = s.handle(b); e != nil {
			return nil, e
		}
	}
}
func login(ctx context.Context, l Link, c Credential, update func(State)) (*session, error) {
	s := &session{link: l, update: update}
	token, e := s.initial(ctx)
	if e != nil {
		return nil, e
	}
	key, e := c.key()
	if e != nil {
		return nil, e
	}
	var auth []byte
	s.cipher, auth, e = newCipher(key, token)
	if e != nil {
		return nil, e
	}
	reply, e := s.exchange(ctx, 2, auth[:4], false)
	if e != nil {
		return nil, fmt.Errorf("Sesame login: %w", e)
	}
	if len(reply) < 4 {
		return nil, errors.New("short Sesame login response")
	}
	s.state.Online = true
	s.state.Updated = time.Now()
	s.publish()
	return s, nil
}

// Register enrolls only a device advertising as unregistered. Persist is invoked
// immediately after deriving the credential, before any additional BLE command.
// A registered device must instead be imported; this API never resets a lock.
func Register(ctx context.Context, t Target, dial Dialer, persist func(Credential) error) error {
	l, actual, e := dial(ctx, t)
	if e != nil {
		return e
	}
	defer l.Close()
	if actual.Registered {
		return errors.New("lock already registered: import its local key instead")
	}
	s := &session{link: l}
	if _, e = s.initial(ctx); e != nil {
		return e
	}
	private, e := ecdh.P256().GenerateKey(rand.Reader)
	if e != nil {
		return e
	}
	payload := append([]byte(nil), private.PublicKey().Bytes()[1:]...)
	stamp := make([]byte, 4)
	binary.LittleEndian.PutUint32(stamp, uint32(time.Now().Unix()))
	payload = append(payload, stamp...)
	reply, e := s.exchange(ctx, 1, payload, false)
	if e != nil {
		return e
	}
	if len(reply) < 77 {
		return errors.New("invalid registration response; lock may need recovery in the Sesame app")
	}
	pub, e := ecdh.P256().NewPublicKey(append([]byte{4}, reply[13:77]...))
	if e != nil {
		return errors.New("invalid Sesame registration public key")
	}
	shared, e := private.ECDH(pub)
	if e != nil {
		return e
	}
	return persist(Credential{Secret: hex.EncodeToString(shared[:16])})
}
func Probe(ctx context.Context, t Target, c Credential, dial Dialer) error {
	l, actual, e := dial(ctx, t)
	if e != nil {
		return e
	}
	defer l.Close()
	if !actual.Registered {
		return errors.New("lock is unregistered; register it first")
	}
	_, e = login(ctx, l, c, nil)
	return e
}

type operation struct {
	ctx   context.Context
	name  string
	reply chan error
}
type Client struct {
	target     Target
	credential Credential
	dial       Dialer
	ops        chan operation
	mu         sync.RWMutex
	state      State
}

func NewClient(t Target, c Credential, dial Dialer) *Client {
	return &Client{target: t, credential: c, dial: dial, ops: make(chan operation)}
}
func (c *Client) State() State { c.mu.RLock(); defer c.mu.RUnlock(); return c.state }
func (c *Client) set(s State)  { c.mu.Lock(); c.state = s; c.mu.Unlock() }
func (c *Client) Action(ctx context.Context, name string) error {
	switch name {
	case "lock", "unlock", "set-lock", "set-unlock", "set-boundary":
	default:
		return errors.New("unsupported Sesame action")
	}
	if !c.State().Online {
		return errors.New("physical lock is offline")
	}
	op := operation{ctx, name, make(chan error, 1)}
	select {
	case c.ops <- op:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case e := <-op.reply:
		return e
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *Client) Run(ctx context.Context) {
	defer c.set(State{Error: "physical lock stopped", Updated: time.Now()})
	for ctx.Err() == nil {
		e := c.runSession(ctx)
		msg := "physical lock disconnected"
		if e != nil {
			msg = e.Error()
		}
		c.set(State{Error: msg, Updated: time.Now()})
		if pause(ctx, 3*time.Second) != nil {
			return
		}
	}
}
func (c *Client) runSession(ctx context.Context) error {
	connect, cancel := context.WithTimeout(ctx, 20*time.Second)
	l, actual, e := c.dial(connect, c.target)
	if e != nil {
		cancel()
		return e
	}
	defer l.Close()
	if !actual.Registered {
		cancel()
		return errors.New("Sesame was reset; enroll it again")
	}
	s, e := login(connect, l, c.credential, c.set)
	cancel()
	if e != nil {
		return e
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-l.Done():
			return e
		case p, ok := <-l.Packets():
			if !ok {
				return errors.New("Sesame notification stream ended")
			}
			b, e := s.decode(p)
			if e != nil {
				return e
			}
			if b != nil {
				if e = s.handle(b); e != nil {
					return e
				}
			}
		case op := <-c.ops:
			if e = op.ctx.Err(); e != nil {
				op.reply <- e
				continue
			}
			command, cancel := context.WithTimeout(op.ctx, 8*time.Second)
			// Service shutdown also cancels an in-flight web/HAP operation.
			stop := context.AfterFunc(ctx, cancel)
			e = s.action(command, op.name)
			stop()
			cancel()
			op.reply <- e
			var rejected *ResultError
			if e != nil && !errors.As(e, &rejected) {
				return e
			}
		}
	}
}
func (s *session) action(ctx context.Context, name string) error {
	var item byte
	var payload []byte
	switch name {
	case "lock":
		item = 82
		payload = []byte{0, 14}
	case "unlock":
		item = 83
		payload = []byte{0, 14}
	case "set-lock", "set-unlock":
		if s.state.Position == nil || s.state.LockPosition == nil || s.state.UnlockPosition == nil {
			return &ResultError{80, 6}
		}
		locked, unlocked := *s.state.LockPosition, *s.state.UnlockPosition
		if name == "set-lock" {
			locked = *s.state.Position
		} else {
			unlocked = *s.state.Position
		}
		item = 80
		payload = append(short(locked), short(unlocked)...)
	case "set-boundary":
		if s.state.Boundary == nil || s.state.Position == nil {
			return &ResultError{20, 2}
		}
		item = 20
		payload = short(*s.state.Position)
	default:
		return errors.New("unsupported Sesame action")
	}
	_, e := s.exchange(ctx, item, payload, true)
	if e != nil {
		return e
	}
	if item == 80 {
		s.state.LockPosition = i16(payload[:2])
		s.state.UnlockPosition = i16(payload[2:])
	}
	if item == 20 {
		s.state.Boundary = i16(payload)
	}
	s.publish()
	return nil
}
