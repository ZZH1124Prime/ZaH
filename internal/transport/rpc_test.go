package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func startServer(t *testing.T, h Handler) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	dir, err := os.MkdirTemp("", "zah-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "manager.sock")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	s := Server{Handler: h, OnReady: func() { close(ready) }, RequestTimeout: time.Second}
	go func() { done <- s.Serve(ctx, socket) }()
	select {
	case <-ready:
	case err = <-done:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("server readiness timeout")
	}
	t.Cleanup(cancel)
	return socket, cancel, done
}
func echo(_ context.Context, method string, p json.RawMessage) (any, error) {
	if method == "echo" {
		return p, nil
	}
	return nil, &Error{Code: -32601, Message: "method not found"}
}
func TestCallConcurrentAndShutdown(t *testing.T) {
	socket, cancel, done := startServer(t, echo)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var result map[string]string
			err := Call(context.Background(), socket, "echo", map[string]string{"name": "中文 路径"}, &result)
			if err != nil || result["name"] != "中文 路径" {
				t.Errorf("result %v error %v", result, err)
			}
		}()
	}
	wg.Wait()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown stuck")
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("socket not removed")
	}
}
func TestHandshakeRequired(t *testing.T) {
	socket, _, _ := startServer(t, echo)
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	err = writeFrame(conn, []byte(`{"jsonrpc":"2.0","id":1,"method":"echo","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := readFrame(conn)
	if err != nil {
		t.Fatal(err)
	}
	var r response
	if err = json.Unmarshal(body, &r); err != nil || r.Error == nil || r.Error.Code != -32001 {
		t.Fatalf("%s %v", body, err)
	}
}
func TestFrameBoundsAndShortIO(t *testing.T) {
	var b bytes.Buffer
	payload := []byte(`{"x":"y"}`)
	if err := writeFrame(&b, payload); err != nil {
		t.Fatal(err)
	}
	got, err := readFrame(&b)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal(err)
	}
	for _, size := range []uint32{0, MaxFrameBytes + 1} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], size)
		if _, err = readFrame(bytes.NewReader(header[:])); err == nil {
			t.Fatal("bad size accepted")
		}
	}
	if _, err = readFrame(bytes.NewReader([]byte{0, 0, 0, 3, 'a'})); err == nil {
		t.Fatal("truncated frame accepted")
	}
}
func TestBadRequestsAndNotifications(t *testing.T) {
	socket, _, _ := startServer(t, echo)
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var h HelloResult
	if err = exchange(conn, 1, "protocol.hello", hello{1}, &h); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"jsonrpc":"2.0","id":1,"method":"echo"} {}`, `{"jsonrpc":"2.0","id":{},"method":"echo"}`, `[]`} {
		if err = writeFrame(conn, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		body, e := readFrame(conn)
		if e != nil {
			t.Fatal(e)
		}
		var r response
		if json.Unmarshal(body, &r) != nil || r.Error == nil {
			t.Fatalf("accepted invalid request: %s", body)
		}
	}
	if err = writeFrame(conn, []byte(`{"jsonrpc":"2.0","method":"echo","params":{}}`)); err != nil {
		t.Fatal(err)
	}
	var result map[string]int
	if err = exchange(conn, 2, "echo", map[string]int{"n": 4}, &result); err != nil || result["n"] != 4 {
		t.Fatalf("notification incorrectly responded: %v %v", result, err)
	}
}
func TestErrorRedactionAndBoundedResponse(t *testing.T) {
	socket, _, _ := startServer(t, func(_ context.Context, m string, _ json.RawMessage) (any, error) {
		switch m {
		case "secret":
			return nil, errors.New("password=never disclose")
		case "large":
			return strings.Repeat("x", MaxFrameBytes), nil
		case "panic":
			panic("private data")
		}
		return nil, nil
	})
	for method, code := range map[string]int{"secret": -32603, "large": -32003, "panic": -32603} {
		err := Call(context.Background(), socket, method, nil, nil)
		var rpcErr *Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != code || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "password") {
			t.Fatalf("%s: %v", method, err)
		}
	}
}
func TestPreserveExistingFile(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "sock")
	if err := os.WriteFile(socket, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Serve(context.Background(), socket, echo); err == nil {
		t.Fatal("regular file replaced")
	}
	got, err := os.ReadFile(socket)
	if err != nil || string(got) != "keep" {
		t.Fatal("existing file changed")
	}
}

func TestSchemaLimitsMatchGeneratedContract(t *testing.T) {
	raw, err := os.ReadFile("../../schemas/rpc.json")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Limits struct {
			Version     int `json:"protocol_version"`
			Frame       int `json:"max_frame_bytes"`
			Connections int `json:"max_connections"`
		} `json:"x-zah"`
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s.Limits.Version != ProtocolVersion || s.Limits.Frame != MaxFrameBytes || s.Limits.Connections != MaxConnections {
		t.Fatal("run go generate: schema and Go contract differ")
	}
}
