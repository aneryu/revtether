//go:build unix

package relay

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"revtether/internal/ui"
)

type Snapshot struct {
	Kind    string          `json:"kind"`
	Devices []ui.DeviceView `json:"devices"`
	Logs    []string        `json:"logs"`
}

type command struct {
	Op string `json:"op"`
	ID string `json:"id,omitempty"`
	On *bool  `json:"on,omitempty"`
}

type ownerInfo struct {
	PID  int    `json:"pid"`
	Kind string `json:"kind"`
}

type HubClient struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
	mu   sync.Mutex
}

func DialHub(dir string) (*HubClient, error) {
	conn, err := net.DialTimeout("unix", filepath.Join(dir, sockName), time.Second)
	if err != nil {
		return nil, err
	}
	return &HubClient{
		conn: conn,
		enc:  json.NewEncoder(conn),
		dec:  json.NewDecoder(conn),
	}, nil
}

func (c *HubClient) Recv() (Snapshot, error) {
	var snap Snapshot
	err := c.dec.Decode(&snap)
	return snap, err
}

func (c *HubClient) SetEnabled(id string, on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(command{Op: "set_enabled", ID: id, On: &on})
}

func (c *HubClient) Close() error {
	return c.conn.Close()
}

type hubPeer struct {
	conn net.Conn
	mu   sync.Mutex
}

func (p *hubPeer) send(snap Snapshot) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(time.Second))
	return json.NewEncoder(p.conn).Encode(snap)
}

func ServeHub(ctx context.Context, dir, kind string, disp *ui.Display, ctrl *Control) error {
	ln, err := listenHub(dir)
	if err != nil {
		return err
	}
	return serveHub(ctx, ln, dir, kind, disp, ctrl)
}

func listenHub(dir string) (net.Listener, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, sockName)
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return ln, nil
}

func serveHub(ctx context.Context, ln net.Listener, dir, kind string, disp *ui.Display, ctrl *Control) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		_ = ln.Close()
		_ = os.Remove(filepath.Join(dir, sockName))
	}()

	stopAccept := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stopAccept()

	s := &hubServer{
		kind: kind,
		disp: disp,
		ctrl: ctrl,
		poke: make(chan struct{}, 1),
	}
	defer s.closePeers()
	go s.loop(ctx)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		p := &hubPeer{conn: conn}
		s.add(p)
		_ = p.send(s.snapshot())
		go s.read(p)
	}
}

func (s *hubServer) closePeers() {
	s.mu.Lock()
	peers := s.peers
	s.peers = nil
	s.mu.Unlock()
	for _, p := range peers {
		_ = p.conn.Close()
	}
}

type hubServer struct {
	kind string
	disp *ui.Display
	ctrl *Control

	mu    sync.Mutex
	peers []*hubPeer
	poke  chan struct{}
}

func (s *hubServer) loop(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.broadcast()
		case <-s.poke:
			s.broadcast()
		}
	}
}

func (s *hubServer) snapshot() Snapshot {
	views := s.disp.LastSnapshot()
	for i := range views {
		on := s.ctrl.Enabled(views[i].ID)
		views[i].Enabled = on
		if !on {
			views[i].State = ui.StateOff
			views[i].Detail = ""
		}
	}
	return Snapshot{Kind: s.kind, Devices: views, Logs: s.disp.Logs()}
}

func (s *hubServer) add(p *hubPeer) {
	s.mu.Lock()
	s.peers = append(s.peers, p)
	s.mu.Unlock()
}

func (s *hubServer) drop(p *hubPeer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.peers[:0]
	for _, q := range s.peers {
		if q != p {
			out = append(out, q)
		}
	}
	s.peers = out
	_ = p.conn.Close()
}

func (s *hubServer) broadcast() {
	snap := s.snapshot()
	s.mu.Lock()
	peers := append([]*hubPeer(nil), s.peers...)
	s.mu.Unlock()
	for _, p := range peers {
		if err := p.send(snap); err != nil {
			s.drop(p)
		}
	}
}

func (s *hubServer) bump() {
	if s.poke == nil {
		return
	}
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

func (s *hubServer) read(p *hubPeer) {
	defer s.drop(p)
	dec := json.NewDecoder(p.conn)
	for {
		var cmd command
		if err := dec.Decode(&cmd); err != nil {
			return
		}
		if cmd.Op == "set_enabled" && cmd.On != nil {
			s.ctrl.SetEnabled(cmd.ID, *cmd.On)
			s.bump()
		}
	}
}

func writeOwner(dir, kind string) error {
	b, err := json.Marshal(ownerInfo{PID: os.Getpid(), Kind: kind})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ownerName), b, 0o644)
}

func readOwner(dir string) ownerInfo {
	b, err := os.ReadFile(filepath.Join(dir, ownerName))
	if err != nil {
		return ownerInfo{}
	}
	var info ownerInfo
	_ = json.Unmarshal(b, &info)
	return info
}
