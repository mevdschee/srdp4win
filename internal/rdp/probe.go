package rdp

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// Security protocols of the RDP negotiation (MS-RDPBCGR 2.2.1.1.1).
const (
	protoRDP      = 0x00
	protoSSL      = 0x01
	protoHybrid   = 0x02
	protoRDSTLS   = 0x04
	protoHybridEx = 0x08
	protoRDSAAD   = 0x10
)

// x224Request is a TPKT header, an X.224 Connection Request and an RDP
// negotiation request that offers TLS and NLA.
var x224Request = []byte{
	0x03, 0x00, 0x00, 0x13, // TPKT: version 3, length 19
	0x0E, 0xE0, 0x00, 0x00, 0x00, 0x00, 0x00, // X.224 CR: length 14, dst 0, src 0, class 0
	0x01, 0x00, 0x08, 0x00, // RDP_NEG_REQ, flags 0, length 8
	protoSSL | protoHybrid, 0x00, 0x00, 0x00,
}

// ProbeX224 sends a minimal X.224 Connection Request with an RDP negotiation
// request and reads the Connection Confirm. It returns what the server
// selected: "RDP", "TLS", "NLA (CredSSP)", or the negotiation failure. An
// error means the other side is not reachable or not an RDP server. The
// connection is of no use for a session afterwards.
func ProbeX224(conn net.Conn, timeout time.Duration) (string, error) {
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
		defer conn.SetDeadline(time.Time{})
	}
	if _, err := conn.Write(x224Request); err != nil {
		return "", fmt.Errorf("send connection request: %w", err)
	}
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return "", fmt.Errorf("read connection confirm: %w", err)
	}
	if hdr[0] != 0x03 {
		return "", fmt.Errorf("not an RDP server (answer starts with % x)", hdr[:])
	}
	n := int(binary.BigEndian.Uint16(hdr[2:]))
	if n < 11 || n > 1024 {
		return "", fmt.Errorf("not an RDP server (TPKT length %d)", n)
	}
	body := make([]byte, n-4)
	if _, err := io.ReadFull(conn, body); err != nil {
		return "", fmt.Errorf("read connection confirm: %w", err)
	}
	if body[1]&0xF0 != 0xD0 {
		return "", fmt.Errorf("not an RDP server (X.224 TPDU %#02x instead of a connection confirm)", body[1])
	}
	// After the 7 byte X.224 header: an optional 8 byte negotiation answer.
	if len(body) < 15 {
		return "RDP (no negotiation)", nil
	}
	value := binary.LittleEndian.Uint32(body[11:15])
	switch body[7] {
	case 0x02: // RDP_NEG_RSP
		return protocolName(value), nil
	case 0x03: // RDP_NEG_FAILURE
		return fmt.Sprintf("negotiation failure %d (%s)", value, negFailureText(value)), nil
	}
	return fmt.Sprintf("unknown negotiation answer %#02x", body[7]), nil
}

func protocolName(p uint32) string {
	switch p {
	case protoRDP:
		return "RDP"
	case protoSSL:
		return "TLS"
	case protoHybrid:
		return "NLA (CredSSP)"
	case protoRDSTLS:
		return "RDSTLS"
	case protoHybridEx:
		return "NLA (CredSSP, early user authorization)"
	case protoRDSAAD:
		return "RDS AAD"
	}
	return fmt.Sprintf("protocol %#x", p)
}

func negFailureText(code uint32) string {
	switch code {
	case 1:
		return "the server requires TLS"
	case 2:
		return "the server does not allow TLS"
	case 3:
		return "the server has no certificate"
	case 4:
		return "inconsistent flags"
	case 5:
		return "the server requires NLA"
	case 6:
		return "the server requires TLS with user authentication"
	}
	return "unknown"
}
