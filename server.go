package brts

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

type Server struct {
	IdleTimeout time.Duration

	address   string
	waitGroup *sync.WaitGroup
	mu        *sync.Mutex
	clients   map[*Client]struct{}
	signalCh  chan os.Signal

	onServerStarted  func(addr *net.TCPAddr)
	onServerStopped  func()
	onNewConnection  func(c *Client)
	onConnectionLost func(c *Client)
	onMessageReceive func(c *Client, data []byte)
}

type Client struct {
	Conn net.Conn

	idleTimeout time.Duration
	closeCh     chan struct{}
}

type accepted struct {
	conn net.Conn
	err  error
}

func Create(address string) *Server {
	server := &Server{
		IdleTimeout: 10 * time.Minute,

		address:   address,
		waitGroup: &sync.WaitGroup{},
		mu:        &sync.Mutex{},
		clients:   make(map[*Client]struct{}),
		signalCh:  make(chan os.Signal),

		onServerStarted:  func(addr *net.TCPAddr) {},
		onServerStopped:  func() {},
		onNewConnection:  func(c *Client) {},
		onConnectionLost: func(c *Client) {},
		onMessageReceive: func(c *Client, data []byte) {},
	}
	return server
}

func newClient(conn net.Conn, timeout time.Duration) *Client {
	client := &Client{
		Conn:        conn,
		idleTimeout: timeout,
		closeCh:     make(chan struct{}),
	}
	return client
}

func (s *Server) Start() error {
	addr, _ := net.ResolveTCPAddr("tcp", s.address)
	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return err
	}

	go s.onServerStarted(addr)

	defer func() {
		listener.Close()
		s.onServerStopped()
	}()

	signal.Notify(s.signalCh, os.Interrupt, os.Kill, syscall.SIGTERM, syscall.SIGINT)

	c := make(chan accepted, 1)
	for {
		go func() {
			conn, err := listener.Accept()
			c <- accepted{conn, err}
		}()

		select {
		case accept := <-c:
			if accept.err != nil {
				log.Printf("error accepting connection %v", err)
				continue
			}
			client := newClient(accept.conn, s.IdleTimeout)
			s.waitGroup.Add(1)
			go s.listen(client)

		case <-s.signalCh:
			log.Println("shutting down server...")
			listener.Close()
			s.closeConnections()
			s.waitGroup.Wait()
			return nil
		}
	}
}

func (s *Server) listen(c *Client) {
	s.addClient(c)
	s.onNewConnection(c)

	defer func() {
		c.Conn.Close()
		s.waitGroup.Done()
		s.removeClient(c)
		s.onConnectionLost(c)
		fmt.Println("debug: Client.Listen() gorutine closed")
	}()

	c.updateDeadline()

	timeout := time.After(c.idleTimeout)
	scrCh := make(chan bool)
	scanner := bufio.NewScanner(c)

	for {
		go func(scanCh chan bool) {
			result := scanner.Scan()
			if !result {
				c.closeCh <- struct{}{}
			} else {
				scanCh <- result
			}
		}(scrCh)

		select {
		case scanned := <-scrCh:
			if !scanned {
				if err := scanner.Err(); err != nil {
					fmt.Printf("%v\n", err)
					return
				}
				break
			}
			b := scanner.Bytes()
			timeout = time.After(c.idleTimeout)
			s.onMessageReceive(c, b)

		case <-timeout:
			log.Printf("timeout: %v\n", c.Conn.RemoteAddr())
			return

		case <-c.closeCh:
			return
		}
	}
}

func (c *Client) updateDeadline() {
	idleDeadline := time.Now().Add(c.idleTimeout)
	c.Conn.SetDeadline(idleDeadline)
}

func (s *Server) Shutdown() {
	s.signalCh <- syscall.SIGINT
}

func (s *Server) closeConnections() {
	defer s.mu.Unlock()
	s.mu.Lock()
	for c := range s.clients {
		if c != nil {
			c.Close()
		}
	}
}

func (s *Server) addClient(c *Client) {
	defer s.mu.Unlock()
	s.mu.Lock()
	s.clients[c] = struct{}{}
}

func (s *Server) removeClient(c *Client) {
	defer s.mu.Unlock()
	s.mu.Lock()
	delete(s.clients, c)
}

func (c *Client) Read(p []byte) (n int, err error) {
	c.updateDeadline()
	n, err = c.Conn.Read(p)
	return
}

func (c *Client) Close() (err error) {
	err = c.Conn.Close()
	return
}

func (s *Server) Clients() map[*Client]struct{} {
	return s.clients
}

func (s *Server) OnServerStarted(callback func(addr *net.TCPAddr)) {
	s.onServerStarted = callback
}

func (s *Server) OnServerStopped(callback func()) {
	s.onServerStopped = callback
}

func (s *Server) OnNewConnection(callback func(c *Client)) {
	s.onNewConnection = callback
}

func (s *Server) OnConnectionLost(callback func(c *Client)) {
	s.onConnectionLost = callback
}

func (s *Server) OnMessageReceive(callback func(c *Client, data []byte)) {
	s.onMessageReceive = callback
}