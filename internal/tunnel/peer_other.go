//go:build !windows

package tunnel

import "net"

// ownProcess always passes: the check exists on Windows only.
func ownProcess(local, peer *net.TCPAddr) bool { return true }
