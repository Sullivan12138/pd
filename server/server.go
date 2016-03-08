package server

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/juju/errors"
	"github.com/ngaut/log"
)

const (
	etcdTimeout = time.Second * 3
)

type Server struct {
	cfg *Config

	listener net.Listener

	client *clientv3.Client

	isLeader int64

	wg sync.WaitGroup

	connsLock sync.Mutex
	conns     map[*conn]struct{}

	closed int64
}

func NewServer(cfg *Config) (*Server, error) {
	log.Infof("create etcd v3 client with endpoints %v", cfg.EtcdAddrs)
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   cfg.EtcdAddrs,
		DialTimeout: etcdTimeout,
	})

	if err != nil {
		return nil, errors.Trace(err)
	}

	log.Infof("listening address %s", cfg.Addr)
	l, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		client.Close()
		return nil, errors.Trace(err)
	}

	s := &Server{
		cfg:      cfg,
		listener: l,
		client:   client,
		isLeader: 0,
		conns:    make(map[*conn]struct{}),
		closed:   0,
	}

	return s, nil
}

// Close closes the server.
func (s *Server) Close() {
	if !atomic.CompareAndSwapInt64(&s.closed, 0, 1) {
		// server is already closed
		return
	}

	s.closeAllConnections()

	if s.listener != nil {
		s.listener.Close()
	}

	if s.client != nil {
		s.client.Close()
	}

	s.wg.Wait()
}

// IsClosed checks whether server is closed or not.
func (s *Server) IsClosed() bool {
	return atomic.LoadInt64(&s.closed) == 1
}

// ListeningAddr returns listen address.
func (s *Server) ListeningAddr() string {
	return s.listener.Addr().String()
}

func (s *Server) Run() error {
	s.wg.Add(1)
	go s.leaderLoop()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			log.Errorf("accept err %s", err)
			break
		}

		if !s.IsLeader() {
			log.Infof("server %s is not leader, close connection directly", s.cfg.Addr)
			continue
		}

		c := newConn(s, conn)
		s.wg.Add(1)
		go c.run()
	}

	return nil
}

func (s *Server) closeAllConnections() {
	s.connsLock.Lock()
	defer s.connsLock.Unlock()

	// TODO: should we send an error message before close?
	for conn, _ := range s.conns {
		conn.Close()
	}

	s.conns = make(map[*conn]struct{})
}
