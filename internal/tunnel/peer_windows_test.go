//go:build windows

package tunnel

import (
	"net"
	"testing"
)

// A connection made by the test itself belongs to our own process.
func TestOwnProcess(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	local, peer := server.LocalAddr().(*net.TCPAddr), server.RemoteAddr().(*net.TCPAddr)
	if !ownProcess(local, peer) {
		t.Fatal("own connection refused")
	}
	if _, ok := tcpTable(); !ok {
		t.Skip("no TCP table here, check is skipped")
	}
	// A connection that does not exist has no owner.
	if ownProcess(local, &net.TCPAddr{IP: peer.IP, Port: 1}) {
		t.Fatal("unknown connection accepted")
	}
}
