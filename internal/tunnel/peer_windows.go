//go:build windows

package tunnel

import (
	"encoding/binary"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

const (
	afInet               = 2
	tcpTableOwnerPidAll  = 5 // TCP_TABLE_OWNER_PID_ALL
	errInsufficientBufer = 122
)

var (
	iphlpapi                = syscall.NewLazyDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")

	peerCheckOff sync.Once
)

// tcpRowOwnerPid is MIB_TCPROW_OWNER_PID. Addresses and ports are in network
// byte order; a port sits in the first two bytes of its field.
type tcpRowOwnerPid struct {
	State      uint32
	LocalAddr  [4]byte
	LocalPort  [4]byte
	RemoteAddr [4]byte
	RemotePort [4]byte
	OwningPid  uint32
}

// ownProcess reports whether the connection that arrived on local from peer
// was made by this process. The peer's socket is looked up in the system's
// TCP table, which names the owning process of every connection. When the
// table cannot be read (Wine) the check is skipped.
func ownProcess(local, peer *net.TCPAddr) bool {
	rows, ok := tcpTable()
	if !ok {
		return true
	}
	pip, lip := peer.IP.To4(), local.IP.To4()
	if pip == nil || lip == nil {
		return false
	}
	for i := range rows {
		r := &rows[i]
		// The peer's own row: its local side is our remote side.
		if int(binary.BigEndian.Uint16(r.LocalPort[:2])) != peer.Port ||
			int(binary.BigEndian.Uint16(r.RemotePort[:2])) != local.Port ||
			!pip.Equal(r.LocalAddr[:]) || !lip.Equal(r.RemoteAddr[:]) {
			continue
		}
		if r.OwningPid == 0 {
			// Wine can list the connection without knowing its owner.
			skipPeerCheck("no owner in the TCP table")
			return true
		}
		return r.OwningPid == uint32(os.Getpid())
	}
	return false
}

// tcpTable reads the IPv4 connection table with owner process IDs.
func tcpTable() ([]tcpRowOwnerPid, bool) {
	if err := procGetExtendedTcpTable.Find(); err != nil {
		skipPeerCheck(err.Error())
		return nil, false
	}
	size := uint32(16 * 1024)
	for try := 0; try < 8; try++ {
		buf := make([]byte, size)
		ret, _, _ := procGetExtendedTcpTable.Call(
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&size)),
			0, // unsorted
			afInet,
			tcpTableOwnerPidAll,
			0,
		)
		if ret == errInsufficientBufer {
			size += 4096 // the table can grow between calls
			continue
		}
		if ret != 0 {
			skipPeerCheck(syscall.Errno(ret).Error())
			return nil, false
		}
		n := binary.LittleEndian.Uint32(buf[:4])
		rowSize := uint32(unsafe.Sizeof(tcpRowOwnerPid{}))
		if n == 0 || 4+n*rowSize > uint32(len(buf)) {
			skipPeerCheck("empty TCP table")
			return nil, false
		}
		return unsafe.Slice((*tcpRowOwnerPid)(unsafe.Pointer(&buf[4])), n), true
	}
	skipPeerCheck("TCP table keeps growing")
	return nil, false
}

// skipPeerCheck logs once that the process check does not work here.
func skipPeerCheck(why string) {
	peerCheckOff.Do(func() {
		slog.Warn("tunnel: cannot check the process of connections, check skipped", "reason", why)
	})
}
