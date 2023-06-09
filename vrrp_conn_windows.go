//go:build windows

package govrrp

import (
	"net"
)

// NewIPv4VRRPMsgConn  not implemented
func NewIPv4VRRPMsgConn(ift *net.Interface, src, dst net.IP) (VRRPMsgConnection, error) {
	panic("NewIPv4VRRPMsgConn not implemented")
}

// NewIPv6VRRPMsgCon   not implemented
func NewIPv6VRRPMsgCon(ift *net.Interface, src, dst net.IP) (VRRPMsgConnection, error) {
	panic("NewIPv6VRRPMsgCon not implemented")
}
