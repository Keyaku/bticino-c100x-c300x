package sip

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// fakeSIPServer acepta una conexión TCP y responde 200 a cualquier REGISTER.
func fakeSIPServer(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		r := bufio.NewReader(conn)
		for {
			m, err := ReadSIPFrame(r)
			if err != nil {
				return
			}
			if m.Method == "REGISTER" {
				conn.Write([]byte("SIP/2.0 200 Registration successful\r\nCSeq: " + m.Headers["CSeq"] + "\r\nContent-Length: 0\r\n\r\n"))
			}
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func TestTCPTransport_RegistraYRecibe(t *testing.T) {
	addr, stop := fakeSIPServer(t)
	defer stop()
	tr, err := NewTCPTransport(addr, logrus.New())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer tr.Close()
	reg := BuildAnswererRegister("bridge", "YOUR_GATEWAY_ID.bs.iotleg.com", tr.LocalHost(), tr.LocalPort(), 3600, "z9hG4bK1", "cid1", "ft1", 1)
	if err := tr.Send(reg); err != nil {
		t.Fatalf("send: %v", err)
	}
	done := make(chan *SIPMessage, 1)
	go func() { m, _ := tr.Recv(); done <- m }()
	select {
	case m := <-done:
		if m.StatusCode != "200" {
			t.Fatalf("esperaba 200, got %q", m.StatusCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout esperando 200")
	}
	if !strings.Contains(reg, "transport=tcp") {
		t.Error("REGISTER sin transport=tcp")
	}
}
