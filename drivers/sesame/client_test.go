package sesame

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLink struct {
	packets chan []byte
	done    chan error
	rx      assembler
	write   func(byte, []byte) error
	mu      sync.Mutex
	closed  bool
}

func newLink() *fakeLink {
	f := &fakeLink{packets: make(chan []byte, 64), done: make(chan error, 1)}
	f.emit(1, []byte{8, 14, 0x11, 0x22, 0x33, 0x44})
	return f
}
func (f *fakeLink) emit(kind byte, b []byte) {
	fs, _ := fragments(kind, b)
	for _, v := range fs {
		f.packets <- v
	}
}
func (f *fakeLink) Write(ctx context.Context, b []byte) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	kind, p, e := f.rx.feed(b)
	if e != nil {
		return e
	}
	if kind != 0 {
		return f.write(kind, p)
	}
	return nil
}
func (f *fakeLink) Packets() <-chan []byte { return f.packets }
func (f *fakeLink) Done() <-chan error     { return f.done }
func (f *fakeLink) Close()                 { f.mu.Lock(); f.closed = true; f.mu.Unlock() }

var testTarget = Target{UUID: "00112233-4455-6677-8899-aabbccddeeff", Model: "sesame_6_pro", Adapter: "hci0"}
var testKey = Credential{Secret: "000102030405060708090a0b0c0d0e0f"}

func readySession(t *testing.T) (*session, *fakeLink, *sessionCipher) {
	t.Helper()
	f := newLink()
	key, _ := testKey.key()
	peer, auth, _ := newCipher(key, unhex("11223344"))
	f.write = func(kind byte, b []byte) error {
		if kind != 1 || !bytes.Equal(b, append([]byte{2}, auth[:4]...)) {
			return errors.New("wrong login")
		}
		reply, _ := peer.encrypt([]byte{7, 2, 0, 0, 0, 0, 0})
		f.emit(2, reply)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, e := login(ctx, f, testKey, nil)
	if e != nil {
		t.Fatal(e)
	}
	return s, f, peer
}
func TestLoginCalibrationAndCommandAcknowledgement(t *testing.T) {
	s, f, peer := readySession(t)
	s.state.publish(81, []byte{0, 0, 0, 128, 45, 0, 16})
	s.state.publish(80, []byte{90, 0, 0, 0, 0, 0})
	s.state.publish(20, []byte{30, 0})
	commands := [][]byte{{80, 45, 0, 0, 0}, {80, 45, 0, 45, 0}, {20, 45, 0}, {83, 0, 14}, {82, 0, 14}}
	i := 0
	f.write = func(kind byte, b []byte) error {
		if kind != 2 {
			return errors.New("plaintext command")
		}
		p, e := peer.decrypt(b)
		if e != nil {
			return e
		}
		if !bytes.Equal(p, commands[i]) {
			t.Errorf("command %d: %x", i, p)
		}
		i++
		reply, _ := peer.encrypt([]byte{7, p[0], 0})
		f.emit(2, reply)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, action := range []string{"set-lock", "set-unlock", "set-boundary", "unlock", "lock"} {
		if e := s.action(ctx, action); e != nil {
			t.Fatal(e)
		}
	}
	if s.state.Locked {
		t.Fatal("command fabricated physical lock state")
	}
	if *s.state.Boundary != 45 {
		t.Fatal("boundary not updated")
	}
	f.write = func(kind byte, b []byte) error {
		p, e := peer.decrypt(b)
		if e != nil {
			return e
		}
		reply, _ := peer.encrypt([]byte{7, p[0], 7})
		f.emit(2, reply)
		return nil
	}
	var rejected *ResultError
	if e := s.action(ctx, "unlock"); !errors.As(e, &rejected) || rejected.Code != 7 {
		t.Fatal("did not propagate BUSY", e)
	}
}
func TestSessionRejectsTamperingAndPlaintext(t *testing.T) {
	s, _, peer := readySession(t)
	cipher, _ := peer.encrypt([]byte{8, 81, 0, 0, 0, 128, 1, 0, 16})
	cipher[len(cipher)-1] ^= 1
	fs, _ := fragments(2, cipher)
	if _, e := s.decode(fs[0]); e == nil {
		t.Fatal("accepted tampered notification")
	}
	fs, _ = fragments(1, []byte{8, 81, 0, 0, 0, 128, 1, 0, 16})
	if _, e := s.decode(fs[0]); e == nil {
		t.Fatal("accepted plaintext status")
	}
}
func TestRegistrationAndRegisteredDeviceRefusal(t *testing.T) {
	f := newLink()
	device, _ := ecdh.P256().GenerateKey(rand.Reader)
	var want string
	f.write = func(kind byte, p []byte) error {
		if kind != 1 || len(p) != 69 || p[0] != 1 {
			return errors.New("invalid registration command")
		}
		pub, e := ecdh.P256().NewPublicKey(append([]byte{4}, p[1:65]...))
		if e != nil {
			return e
		}
		shared, e := device.ECDH(pub)
		if e != nil {
			return e
		}
		want = hex.EncodeToString(shared[:16])
		reply := make([]byte, 77)
		copy(reply[13:], device.PublicKey().Bytes()[1:])
		f.emit(1, append([]byte{7, 1, 0}, reply...))
		return nil
	}
	dial := func(context.Context, Target) (Link, Target, error) { return f, testTarget, nil }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	saved := ""
	if e := Register(ctx, testTarget, dial, func(c Credential) error { saved = c.Secret; return nil }); e != nil || saved == "" || saved != want {
		t.Fatal("credential derivation", e)
	}
	registered := testTarget
	registered.Registered = true
	f = newLink()
	f.write = func(byte, []byte) error { t.Fatal("wrote to registered device"); return nil }
	dial = func(context.Context, Target) (Link, Target, error) { return f, registered, nil }
	if e := Register(ctx, registered, dial, func(Credential) error { t.Fatal("overwrote credential"); return nil }); e == nil {
		t.Fatal("registered an owned lock")
	}
}
func TestMissingBoundaryDoesNotWrite(t *testing.T) {
	s, f, _ := readySession(t)
	f.write = func(byte, []byte) error { t.Fatal("unsupported setting sent"); return nil }
	if e := s.action(context.Background(), "set-boundary"); e == nil {
		t.Fatal("accepted unsupported boundary")
	}
}
func TestClientCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dial := func(ctx context.Context, _ Target) (Link, Target, error) {
		<-ctx.Done()
		return nil, Target{}, ctx.Err()
	}
	c := NewClient(testTarget, testKey, dial)
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("client did not stop")
	}
	if c.State().Online {
		t.Fatal("online after stop")
	}
}

func TestClientReceivesManualStateAndDoesNotReplay(t *testing.T) {
	f := newLink()
	key, _ := testKey.key()
	peer, auth, _ := newCipher(key, unhex("11223344"))
	writes := make(chan byte, 8)
	f.write = func(kind byte, b []byte) error {
		if kind == 1 {
			if !bytes.Equal(b, append([]byte{2}, auth[:4]...)) {
				return errors.New("login mismatch")
			}
			reply, _ := peer.encrypt([]byte{7, 2, 0, 0, 0, 0, 0})
			f.emit(2, reply)
			state, _ := peer.encrypt([]byte{8, 81, 0, 0, 0, 128, 90, 0, 18})
			f.emit(2, state)
			return nil
		}
		p, e := peer.decrypt(b)
		if e != nil {
			return e
		}
		writes <- p[0]
		// Deliberately drop the acknowledgement. Client must invalidate this session.
		return nil
	}
	actual := testTarget
	actual.Registered = true
	c := NewClient(testTarget, testKey, func(context.Context, Target) (Link, Target, error) { return f, actual, nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(time.Second)
	for c.State().Position == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s := c.State(); !s.Online || s.Position == nil || *s.Position != 90 || !s.Locked {
		t.Fatalf("manual status not delivered: %+v", s)
	}
	request, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if e := c.Action(request, "unlock"); e == nil {
		t.Fatal("missing ack reported success")
	}
	deadline = time.Now().Add(time.Second)
	for c.State().Online && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.State().Online {
		t.Fatal("timed-out crypto session remained online")
	}
	if len(writes) != 1 || <-writes != 83 {
		t.Fatal("replayed or missed unlock")
	}
}

func TestInitialTokenTimeoutIdentifiesStage(t *testing.T) {
	f := &fakeLink{packets: make(chan []byte), done: make(chan error)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, e := login(ctx, f, testKey, nil)
	if !errors.Is(e, context.DeadlineExceeded) || !strings.Contains(e.Error(), "initial token") {
		t.Fatal("missing stage", e)
	}
}

func TestReconnectUsesNewTokenAndResetsCipher(t *testing.T) {
	var attempts atomic.Int32
	firstReady := make(chan *fakeLink, 1)
	dial := func(ctx context.Context, target Target) (Link, Target, error) {
		n := attempts.Add(1)
		f := newLink()
		if n == 1 {
			firstReady <- f
		}
		token := []byte{byte(n), 0x22, 0x33, 0x44}
		<-f.packets
		f.emit(1, append([]byte{8, 14}, token...))
		key, _ := testKey.key()
		peer, auth, _ := newCipher(key, token)
		f.write = func(kind byte, b []byte) error {
			if kind != 1 || !bytes.Equal(b, append([]byte{2}, auth[:4]...)) {
				return errors.New("reconnect reused login token")
			}
			reply, _ := peer.encrypt([]byte{7, 2, 0, 0, 0, 0, 0})
			f.emit(2, reply)
			state, _ := peer.encrypt([]byte{8, 81, 0, 0, 0, 128, byte(n), 0, 18})
			f.emit(2, state)
			return nil
		}
		target.Registered = true
		return f, target, nil
	}
	c := NewClient(testTarget, testKey, dial)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	await := func(position int16) {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			s := c.State()
			if s.Online && s.Position != nil && *s.Position == position {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("did not authenticate session %d: %+v", position, c.State())
	}
	await(1)
	first := <-firstReady
	first.done <- errors.New("battery removed")
	await(2)
	if attempts.Load() != 2 {
		t.Fatal("unexpected reconnect attempts", attempts.Load())
	}
}
