package server

import (
	"net"
	"sync"
	"time"
)

const connectionOverflowBackoff = 10 * time.Millisecond

// connectionLimitListener bounds the file descriptors and HTTP goroutines
// retained by unauthenticated or idle localhost clients. Overflow connections
// are closed before net/http receives them, with a small backoff to avoid an
// accept/close busy loop under sustained local flooding.
type connectionLimitListener struct {
	net.Listener
	slots chan struct{}
}

func newConnectionLimitListener(listener net.Listener, limit int) net.Listener {
	return &connectionLimitListener{
		Listener: listener,
		slots:    make(chan struct{}, limit),
	}
}

func (l *connectionLimitListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &connectionLimitConnection{
				Conn: connection,
				release: func() {
					<-l.slots
				},
			}, nil
		default:
			_ = connection.Close()
			time.Sleep(connectionOverflowBackoff)
		}
	}
}

type connectionLimitConnection struct {
	net.Conn
	release     func()
	releaseOnce sync.Once
}

func (c *connectionLimitConnection) Close() error {
	err := c.Conn.Close()
	c.releaseOnce.Do(c.release)
	return err
}
