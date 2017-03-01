package turnpike

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var defaultWelcomeDetails = map[string]interface{}{
	"roles": map[string]struct{}{
		"broker": {},
		"dealer": {},
	},
}

type RealmExistsError string

func (e RealmExistsError) Error() string {
	return "realm exists: " + string(e)
}

type NoSuchRealmError string

func (e NoSuchRealmError) Error() string {
	return "no such realm: " + string(e)
}

// AuthenticationError means the peer was unable to authenticate
type AuthenticationError string

func (e AuthenticationError) Error() string {
	return "authentication error: " + string(e)
}

// Router handles new Peers and routes requests to the requested Realm.
type Router interface {
	Accept(Peer) error
	Close() error
	RegisterRealm(URI, *Realm) error
	GetLocalPeer(URI, map[string]interface{}) (Peer, error)
	AddSessionOpenCallback(func(uint, string))
	AddSessionCloseCallback(func(uint, string))
}

// DefaultRouter is the default WAMP router implementation.
type defaultRouter struct {
	realms                map[string]*Realm
	realmsLock            sync.RWMutex
	closing               bool
	closeLock             sync.Mutex
	sessionOpenCallbacks  []func(uint, string)
	sessionCloseCallbacks []func(uint, string)
}

// NewDefaultRouter creates a very basic WAMP router.
func NewDefaultRouter() Router {
	return &defaultRouter{
		realms:                make(map[string]*Realm),
		sessionOpenCallbacks:  []func(uint, string){},
		sessionCloseCallbacks: []func(uint, string){},
	}
}

func (r *defaultRouter) AddSessionOpenCallback(fn func(uint, string)) {
	r.sessionOpenCallbacks = append(r.sessionOpenCallbacks, fn)
}

func (r *defaultRouter) AddSessionCloseCallback(fn func(uint, string)) {
	r.sessionCloseCallbacks = append(r.sessionCloseCallbacks, fn)
}

func (r *defaultRouter) Close() error {
	r.closeLock.Lock()
	if r.closing {
		r.closeLock.Unlock()
		return fmt.Errorf("already closed")
	}
	r.closing = true
	r.closeLock.Unlock()
	r.realmsLock.RLock()
	for _, realm := range r.realms {
		realm.Close()
	}
	r.realmsLock.RUnlock()
	return nil
}

func (r *defaultRouter) RegisterRealm(uri URI, realm *Realm) error {
	r.realmsLock.Lock()
	defer r.realmsLock.Unlock()
	if _, ok := r.realms[string(uri)]; ok {
		return RealmExistsError(uri)
	}
	realm.init()

	r.realms[string(uri)] = realm
	log.Println("registered realm:", uri)
	return nil
}

func (r *defaultRouter) Accept(client Peer) error {
	r.closeLock.Lock()
	defer r.closeLock.Unlock()
	if r.closing {
		logErr(client.Send(&Abort{Reason: ErrSystemShutdown}))
		logErr(client.Close())
		return errors.New("Router is closing, no new connections are allowed")
	}

	msg, err := GetMessageTimeout(client, 5*time.Second)
	if err != nil {
		return err
	}
	log.Printf("%s: %+v", msg.MessageType(), msg)

	hello, ok := msg.(*Hello)
	if !ok {
		logErr(client.Send(&Abort{Reason: URI("wamp.error.protocol_violation")}))
		logErr(client.Close())
		return fmt.Errorf("protocol violation: expected HELLO, received %s", msg.MessageType())
	}

	r.realmsLock.RLock()
	realm, ok := r.realms[string(hello.Realm)]
	r.realmsLock.RUnlock()
	if !ok {
		logErr(client.Send(&Abort{Reason: ErrNoSuchRealm}))
		logErr(client.Close())
		return NoSuchRealmError(hello.Realm)
	}

	welcome, err := realm.handleAuth(client, hello.Details)
	if err != nil {
		abort := &Abort{
			Reason:  ErrAuthorizationFailed, // TODO: should this be AuthenticationFailed?
			Details: map[string]interface{}{"error": err.Error()},
		}
		logErr(client.Send(abort))
		logErr(client.Close())
		return AuthenticationError(err.Error())
	}

	welcome.ID = NewID()

	if welcome.Details == nil {
		welcome.Details = make(map[string]interface{})
	}
	// add default details to welcome message
	for k, v := range defaultWelcomeDetails {
		if _, ok := welcome.Details[k]; !ok {
			welcome.Details[k] = v
		}
	}
	if err := client.Send(welcome); err != nil {
		return err
	}
	log.Println("Established session:", welcome.ID)

	// session details
	welcome.Details["session"] = welcome.ID
	welcome.Details["realm"] = hello.Realm
	sess := &Session{
		Peer:    client,
		ID:      welcome.ID,
		Details: welcome.Details,
		kill:    make(chan URI, 1),
	}
	for _, callback := range r.sessionOpenCallbacks {
		go callback(uint(sess.ID), string(hello.Realm))
	}
	go func() {
		realm.handleSession(sess)
		sess.Close()
		for _, callback := range r.sessionCloseCallbacks {
			go callback(uint(sess.ID), string(hello.Realm))
		}
	}()
	return nil
}

// GetLocalPeer returns an internal peer connected to the specified realm.
func (r *defaultRouter) GetLocalPeer(realmURI URI, details map[string]interface{}) (Peer, error) {
	r.realmsLock.RLock()
	realm, ok := r.realms[string(realmURI)]
	r.realmsLock.RUnlock()
	if !ok {
		return nil, NoSuchRealmError(realmURI)
	}

	// TODO: session open/close callbacks?
	return realm.getPeer(details)
}

func (r *defaultRouter) getTestPeer() Peer {
	peerA, peerB := localPipe()
	go r.Accept(peerA)
	return peerB
}
