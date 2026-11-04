// Package transport implements the local, versioned ZaH JSON-RPC wire protocol.
package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/infra"
)

//go:generate go run ./generate

// Error returns a safe display string for an RPC error.
func (e *Error) Error() string { return fmt.Sprintf("RPC %d: %s", e.Code, e.Message) }

// Handler receives application methods after a successful protocol handshake.
type Handler func(context.Context, string, json.RawMessage) (any, error)
type request = Request
type response = Response
type hello = HelloParams

// Server callbacks run concurrently and must be concurrency-safe. OnConnect and
// OnDisconnect delimit accepted clients; OnReady runs after the socket is ready.
type Server struct {
	Handler                          Handler
	OnConnect, OnDisconnect, OnReady func()
	RequestTimeout                   time.Duration
}

func Serve(ctx context.Context, socket string, handler Handler) error {
	return (&Server{Handler: handler}).Serve(ctx, socket)
}

// Serve listens exclusively on an owned Unix socket. It never listens on TCP.
func (s *Server) Serve(ctx context.Context, socket string) error {
	if s.Handler == nil {
		return errors.New("RPC handler is required")
	}
	if !filepath.IsAbs(socket) || len(socket) > 100 {
		return errors.New("socket path must be absolute and no longer than 100 bytes")
	}
	if err := infra.ValidatePrivateDir(filepath.Dir(socket)); err != nil {
		return err
	}
	lock, err := infra.AcquireLock(socket + ".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	if st, e := os.Lstat(socket); e == nil {
		stat, ok := st.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || st.Mode()&os.ModeSocket == 0 {
			return errors.New("refusing to replace non-owned socket")
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	if s.OnReady != nil {
		s.OnReady()
	}
	slots := make(chan struct{}, MaxConnections)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, e := listener.AcceptUnix()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		if e = checkPeer(conn); e != nil {
			conn.Close()
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		if s.OnConnect != nil {
			s.OnConnect()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				<-slots
				if s.OnDisconnect != nil {
					s.OnDisconnect()
				}
			}()
			defer conn.Close()
			cancelClose := context.AfterFunc(ctx, func() { conn.Close() })
			defer cancelClose()
			s.serveConn(ctx, conn)
		}()
	}
}

func checkPeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var inner error
	err = raw.Control(func(fd uintptr) {
		cred, e := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if e != nil {
			inner = e
			return
		}
		if cred.Uid != uint32(os.Geteuid()) {
			inner = errors.New("foreign peer rejected")
		}
	})
	if err != nil {
		return err
	}
	return inner
}
func (s *Server) serveConn(ctx context.Context, conn *net.UnixConn) {
	timeout := s.RequestTimeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	handshaken := false
	for {
		if ctx.Err() != nil {
			return
		}
		conn.SetReadDeadline(time.Now().Add(timeout))
		body, err := readFrame(conn)
		if err != nil {
			return
		}
		var req request
		d := json.NewDecoder(bytes.NewReader(body))
		d.DisallowUnknownFields()
		err = d.Decode(&req)
		if err != nil || !json.Valid(body) {
			code := -32600
			if !json.Valid(body) {
				code = -32700
			}
			if send(conn, response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &Error{Code: code, Message: "invalid request"}}, timeout) != nil {
				return
			}
			continue
		}
		if req.JSONRPC != "2.0" || req.Method == "" || len(req.Method) > 128 || !validID(req.ID) || !validParams(req.Params) {
			if send(conn, response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &Error{Code: -32600, Message: "invalid request"}}, timeout) != nil {
				return
			}
			continue
		}
		notification := len(req.ID) == 0
		if !handshaken {
			var h hello
			err = json.Unmarshal(req.Params, &h)
			if req.Method != "protocol.hello" || notification || err != nil || h.Version != ProtocolVersion {
				if !notification {
					send(conn, response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: -32001, Message: "protocol.hello version 1 required"}}, timeout)
				}
				return
			}
			handshaken = true
			b, _ := json.Marshal(HelloResult{Version: ProtocolVersion, MaxFrameBytes: MaxFrameBytes, MaxConnections: MaxConnections})
			if send(conn, response{JSONRPC: "2.0", ID: req.ID, Result: b}, timeout) != nil {
				return
			}
			continue
		}
		child, cancel := context.WithTimeout(ctx, timeout)
		result, callErr := invoke(s.Handler, child, req.Method, req.Params)
		cancel()
		if notification {
			continue
		}
		resp := response{JSONRPC: "2.0", ID: req.ID}
		if callErr != nil {
			var rpcErr *Error
			if errors.As(callErr, &rpcErr) {
				resp.Error = rpcErr
			} else if errors.Is(callErr, context.DeadlineExceeded) || errors.Is(callErr, context.Canceled) {
				resp.Error = &Error{Code: -32002, Message: "request cancelled or timed out"}
			} else {
				resp.Error = &Error{Code: -32603, Message: "internal error"}
			}
		} else {
			resp.Result, err = json.Marshal(result)
			if err != nil {
				resp.Error = &Error{Code: -32603, Message: "result encoding failed"}
				resp.Result = nil
			}
		}
		if send(conn, resp, timeout) != nil {
			return
		}
	}
}
func invoke(h Handler, ctx context.Context, method string, p json.RawMessage) (result any, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = &Error{Code: -32603, Message: "internal error"}
		}
	}()
	return h(ctx, method, p)
}
func validID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return false
	}
	switch value.(type) {
	case nil, string, json.Number:
		return true
	}
	return false
}
func validParams(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	b := bytes.TrimSpace(raw)
	return len(b) > 0 && (b[0] == '{' || b[0] == '[')
}
func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxFrameBytes {
		return nil, errors.New("invalid frame length")
	}
	body := make([]byte, size)
	_, err := io.ReadFull(r, body)
	return body, err
}
func writeFrame(w io.Writer, body []byte) error {
	if len(body) == 0 || len(body) > MaxFrameBytes {
		return errors.New("invalid frame length")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, body)
}
func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
func send(conn net.Conn, resp response, timeout time.Duration) error {
	body, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if len(body) > MaxFrameBytes {
		body, _ = json.Marshal(response{JSONRPC: "2.0", ID: resp.ID, Error: &Error{Code: -32003, Message: "response exceeds frame limit"}})
	}
	conn.SetWriteDeadline(time.Now().Add(timeout))
	return writeFrame(conn, body)
}

// Call opens a local connection, checks peer identity, handshakes, then performs
// one request. Mutation retries belong to the application idempotency protocol.
func Call(ctx context.Context, socket, method string, params, result any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	dialer := net.Dialer{}
	c, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer c.Close()
	conn, ok := c.(*net.UnixConn)
	if !ok {
		return errors.New("expected Unix socket")
	}
	if err = checkPeer(conn); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	var h HelloResult
	if err = exchange(conn, 1, "protocol.hello", hello{ProtocolVersion}, &h); err != nil {
		return err
	}
	if h.Version != ProtocolVersion {
		return errors.New("unsupported protocol version")
	}
	err = exchange(conn, 2, method, params, result)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
func exchange(conn net.Conn, id int, method string, params, result any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if params == nil {
		p = nil
	}
	req := request{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprint(id)), Method: method, Params: p}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if err = writeFrame(conn, body); err != nil {
		return err
	}
	body, err = readFrame(conn)
	if err != nil {
		return err
	}
	var resp response
	if err = json.Unmarshal(body, &resp); err != nil {
		return err
	}
	if resp.JSONRPC != "2.0" || string(resp.ID) != fmt.Sprint(id) || (resp.Error != nil && len(resp.Result) > 0) || (resp.Error == nil && len(resp.Result) == 0) {
		return errors.New("invalid RPC response")
	}
	if resp.Error != nil {
		return resp.Error
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, result)
}
