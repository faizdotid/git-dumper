package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

const (
	socks4Version    = 4
	socks4CmdConnect = 1

	socks4Granted        = 90
	socks4Rejected       = 91
	socks4IdentdUnreach  = 92
	socks4IdentdMismatch = 93

	socks4DefaultHandshakeTimeout = 30 * time.Second
)

// socks4DialContext returns a dial function that connects to addr through a
// SOCKS4 proxy. Non-IP hosts are delegated to the proxy (SOCKS4a), so no local
// DNS lookup is needed. The handshake honors context cancellation and the
// dialer timeout.
func socks4DialContext(dialer *net.Dialer, proxyAddr string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, fmt.Errorf("socks4: unsupported network %q", network)
		}

		conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, err
		}

		// Closing the connection unblocks the handshake read when the
		// context is cancelled.
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				conn.Close()
			case <-done:
			}
		}()

		if err := socks4Handshake(conn, addr, dialer.Timeout); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	}
}

// socks4Handshake performs the CONNECT negotiation with the proxy.
func socks4Handshake(conn net.Conn, target string, dialTimeout time.Duration) error {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("socks4: invalid target %q: %w", target, err)
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return fmt.Errorf("socks4: invalid target port %q", portStr)
	}

	handshakeTimeout := dialTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = socks4DefaultHandshakeTimeout
	}
	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return err
	}
	defer conn.SetDeadline(time.Time{})

	req := make([]byte, 0, 9+len(host))
	req = append(req, socks4Version, socks4CmdConnect)
	req = binary.BigEndian.AppendUint16(req, uint16(port))

	ip := net.ParseIP(host)
	socks4a := ip.To4() == nil
	if socks4a {
		req = append(req, 0, 0, 0, 1) // SOCKS4a: proxy resolves the hostname
	} else {
		req = append(req, ip.To4()...)
	}
	req = append(req, 0) // empty user id
	if socks4a {
		req = append(req, host...)
		req = append(req, 0)
	}

	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("socks4: handshake write failed: %w", err)
	}

	reply := make([]byte, 8)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("socks4: handshake read failed: %w", err)
	}
	if reply[1] != socks4Granted {
		return fmt.Errorf("socks4: %s", socks4Reply(reply[1]))
	}
	return nil
}

func socks4Reply(code byte) string {
	switch code {
	case socks4Rejected:
		return "request rejected or failed"
	case socks4IdentdUnreach:
		return "request rejected: identd is not running"
	case socks4IdentdMismatch:
		return "request rejected: identd could not confirm the user id"
	default:
		return fmt.Sprintf("unexpected reply code %d", code)
	}
}
