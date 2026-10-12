package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type socks4Request struct {
	cmd  byte
	port uint16
	ip   [4]byte
	host string
}

func readUntilNUL(r io.Reader) ([]byte, error) {
	var buf []byte
	var b [1]byte
	for {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return nil, err
		}
		if b[0] == 0 {
			return buf, nil
		}
		buf = append(buf, b[0])
	}
}

func readSocks4Request(r io.Reader) (*socks4Request, error) {
	var head [8]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	req := &socks4Request{
		cmd:  head[1],
		port: binary.BigEndian.Uint16(head[2:4]),
	}
	copy(req.ip[:], head[4:8])

	if _, err := readUntilNUL(r); err != nil { // empty user id
		return nil, err
	}
	if req.ip == [4]byte{0, 0, 0, 1} { // SOCKS4a: hostname follows
		host, err := readUntilNUL(r)
		if err != nil {
			return nil, err
		}
		req.host = string(host)
	}
	return req, nil
}

// fakeSocks4Proxy accepts one connection, reports the request on the returned
// channel and answers with replyCode. When granted, the connection stays open
// (any data read is discarded) until closed by the client.
func fakeSocks4Proxy(t *testing.T, replyCode byte) (net.Listener, <-chan *socks4Request) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	reqCh := make(chan *socks4Request, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		req, err := readSocks4Request(conn)
		if err != nil {
			return
		}
		reqCh <- req

		if _, err := conn.Write([]byte{0, replyCode, 0, 0, 0, 0, 0, 0}); err != nil {
			return
		}
		io.Copy(io.Discard, conn)
	}()
	return ln, reqCh
}

func TestSocks4DialIPv4(t *testing.T) {
	ln, reqCh := fakeSocks4Proxy(t, socks4Granted)
	defer ln.Close()

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	dial := socks4DialContext(dialer, ln.Addr().String())
	conn, err := dial(context.Background(), "tcp", "10.1.2.3:8080")
	if err != nil {
		t.Fatalf("dial: %s", err)
	}
	defer conn.Close()

	req := <-reqCh
	if req.cmd != socks4CmdConnect {
		t.Errorf("cmd = %d, want %d", req.cmd, socks4CmdConnect)
	}
	if req.port != 8080 {
		t.Errorf("port = %d, want 8080", req.port)
	}
	if req.ip != [4]byte{10, 1, 2, 3} {
		t.Errorf("ip = %v, want 10.1.2.3", req.ip)
	}
	if req.host != "" {
		t.Errorf("host = %q, want empty for an IP target", req.host)
	}
}

func TestSocks4DialHostnameUsesSocks4a(t *testing.T) {
	ln, reqCh := fakeSocks4Proxy(t, socks4Granted)
	defer ln.Close()

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	dial := socks4DialContext(dialer, ln.Addr().String())
	conn, err := dial(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatalf("dial: %s", err)
	}
	defer conn.Close()

	req := <-reqCh
	if req.ip != [4]byte{0, 0, 0, 1} {
		t.Errorf("ip = %v, want the SOCKS4a marker 0.0.0.1", req.ip)
	}
	if req.host != "example.com" {
		t.Errorf("host = %q, want example.com", req.host)
	}
	if req.port != 443 {
		t.Errorf("port = %d, want 443", req.port)
	}
}

func TestSocks4DialRejected(t *testing.T) {
	ln, _ := fakeSocks4Proxy(t, socks4Rejected)
	defer ln.Close()

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	dial := socks4DialContext(dialer, ln.Addr().String())
	if _, err := dial(context.Background(), "tcp", "10.1.2.3:80"); err == nil {
		t.Fatal("expected an error for a rejected request")
	} else if !strings.Contains(err.Error(), "rejected") {
		t.Errorf("error = %q, want mention of rejection", err)
	}
}

func TestSocks4DialContextCancellation(t *testing.T) {
	// The proxy accepts but never answers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(5 * time.Second)
	}()

	dialer := &net.Dialer{}
	dial := socks4DialContext(dialer, ln.Addr().String())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := dial(ctx, "tcp", "10.1.2.3:80"); err == nil {
		t.Fatal("expected an error when the context times out")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("dial took %s, context cancellation was not honored", elapsed)
	}
}

func TestSocks4DialUnsupportedNetwork(t *testing.T) {
	dial := socks4DialContext(&net.Dialer{Timeout: time.Second}, "127.0.0.1:1")
	if _, err := dial(context.Background(), "udp", "10.0.0.1:80"); err == nil {
		t.Error("expected an error for an unsupported network")
	}
}

func TestSocks4ReplyMessages(t *testing.T) {
	tests := []struct {
		code byte
		want string
	}{
		{socks4Rejected, "request rejected or failed"},
		{socks4IdentdUnreach, "identd is not running"},
		{socks4IdentdMismatch, "identd could not confirm the user id"},
	}
	for _, tt := range tests {
		if got := socks4Reply(tt.code); !strings.Contains(got, tt.want) {
			t.Errorf("socks4Reply(%d) = %q, want containing %q", tt.code, got, tt.want)
		}
	}
}
