package nats

import (
	"net"
	"sync"
)

type Subscription struct {
	sr *subscriptionRegistry

	sid      uint
	frozen   bool
	maximum  uint
	received uint

	subject string
	queue   string

	Inbox chan *readMessage
}

func (s *Subscription) freeze() {
	if s.frozen {
		panic("subscription is frozen")
	}

	s.frozen = true
}

func (s *Subscription) SetSubject(v string) {
	if s.frozen {
		panic("subscription is frozen")
	}

	s.subject = v
}

func (s *Subscription) SetQueue(v string) {
	if s.frozen {
		panic("subscription is frozen")
	}

	s.queue = v
}

func (s *Subscription) SetMaximum(v uint) {
	if s.frozen {
		panic("subscription is frozen")
	}

	s.maximum = v
}

func (s *Subscription) writeSubscribe() writeObject {
	return &writeSubscribe{
		Sid:     s.sid,
		Subject: s.subject,
		Queue:   s.queue,
	}
}

func (s *Subscription) writeUnsubscribe(includeMaximum bool) writeObject {
	var o = new(writeUnsubscribe)

	o.Sid = s.sid

	if includeMaximum {
		o.Maximum = s.maximum
	}

	return o
}

func (s *Subscription) Subscribe() {
	s.sr.subscribe(s)
}

func (s *Subscription) Unsubscribe() {
	s.sr.unsubscribe(s)

	// Since this subscription is now removed from the registry, it will no
	// longer receive messages and the inbox can be closed
	close(s.Inbox)
}

func (s *Subscription) deliver(m *readMessage) {
	s.received++
	s.Inbox <- m

	// Unsubscribe if the maximum number of messages has been received
	if s.maximum > 0 && s.received >= s.maximum {
		s.Unsubscribe()
	}
}

type subscriptionRegistry struct {
	sync.Mutex
	*Client

	sid uint
	m   map[uint]*Subscription
}

func (sr *subscriptionRegistry) init(c *Client) {
	sr.Client = c
	sr.m = make(map[uint]*Subscription)
}

func (sr *subscriptionRegistry) NewSubscription(sub string) *Subscription {
	var s = new(Subscription)

	sr.Lock()

	s.sr = sr

	sr.sid++
	s.sid = sr.sid

	sr.Unlock()

	s.SetSubject(sub)
	s.Inbox = make(chan *readMessage)

	return s
}

func (sr *subscriptionRegistry) subscribe(s *Subscription) {
	sr.Lock()

	sr.m[s.sid] = s
	s.freeze()

	sr.Unlock()

	sr.Client.Write(s.writeSubscribe())

	if s.maximum > 0 {
		sr.Client.Write(s.writeUnsubscribe(true))
	}

	return
}

func (sr *subscriptionRegistry) unsubscribe(s *Subscription) {
	sr.Lock()

	delete(sr.m, s.sid)

	sr.Unlock()

	sr.Client.Write(s.writeUnsubscribe(false))

	return
}

func (sr *subscriptionRegistry) deliver(m *readMessage) {
	var s *Subscription
	var ok bool

	sr.Lock()
	s, ok = sr.m[m.SubscriptionId]
	sr.Unlock()

	if ok {
		s.deliver(m)
	}
}

type Client struct {
	subscriptionRegistry
	Handshaker

	Addr string
	User string
	Pass string

	cc chan *Connection
	sc chan bool
}

func NewClient(addr string) *Client {
	var t = new(Client)

	t.subscriptionRegistry.init(t)

	t.Addr = addr

	t.cc = make(chan *Connection)

	return t
}

func (t *Client) Write(o writeObject) bool {
	var c *Connection
	var ok bool

	c, ok = <-t.cc
	if !ok {
		return false
	}

	return c.Write(o)
}

func (t *Client) Ping() bool {
	var c *Connection
	var ok bool

	c, ok = <-t.cc
	if !ok {
		return false
	}

	return c.Ping()
}

func (t *Client) runConnection(n net.Conn) error {
	var e error
	var c *Connection
	var dc chan bool

	c = NewConnection(n)
	dc = make(chan bool)

	// Feed connection until it stops
	go func() {
		var stop bool

		for !stop {
			select {
			case <-dc:
				stop = true
			case t.cc <- c:
				// Sweet!
			}
		}
	}()

	// Read messages until EOF
	go func() {
		var m *readMessage

		for m = range c.mc {
			t.deliver(m)
		}
	}()

	e = c.Run()
	dc <- true
	close(t.cc)

	return e
}

func (t *Client) Loop() error {
	var n net.Conn
	var e error

	n, e = net.Dial("tcp", t.Addr)
	if e != nil {
		return e
	}

	n, e = t.Handshake(n)
	if e != nil {
		return e
	}

	return t.runConnection(n)
}
