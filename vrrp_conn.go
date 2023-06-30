package govrrp

import (
	"fmt"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"net"
)

// NewIPv4VRRPMsgConn 创建的IPv4 VRRP虚拟连接
// ift: 工作网口
// src: IP数据包中源地址，应该为工作网口的IP地址
// dst: IP数据包中目的地址，应该为组播地址 VRRPMultiAddrIPv4
func NewIPv4VRRPMsgConn(itf *net.Interface, src, dst net.IP) (VRRPMsgConnection, error) {
	multiAddr := &net.IPAddr{IP: dst}

	conn, err := net.ListenIP("ip4:112", &net.IPAddr{IP: net.IPv4(0, 0, 0, 0)})
	if err != nil {
		return nil, fmt.Errorf("NewIPv4VRRPMsgConn interface %s ip packet listen err, %v", itf.Name, err)
	}

	pc := ipv4.NewPacketConn(conn)
	_ = pc.LeaveGroup(itf, multiAddr)
	if err = pc.JoinGroup(itf, multiAddr); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("NewIPv4VRRPMsgConn interface %s join multicast group err, %v", itf.Name, err)
	}
	// 设置组播回环
	_ = pc.SetMulticastLoopback(true)
	// 设置消息的TTL为255
	_ = pc.SetMulticastTTL(255)
	_ = pc.SetMulticastInterface(itf)
	_ = pc.SetControlMessage(ipv4.FlagTTL|ipv4.FlagSrc|ipv4.FlagDst|ipv4.FlagInterface, true)

	_ = conn.SetReadBuffer(2048)
	_ = conn.SetWriteBuffer(2048)

	return &IPv4VRRPMsgCon{
		itf:    itf,
		local:  src,
		remote: multiAddr,
		pc:     pc,
		buffer: make([]byte, 2048),
	}, nil
}

// IPv4VRRPMsgCon IPv4的VRRP消息组播连接
type IPv4VRRPMsgCon struct {
	itf    *net.Interface   // 工作网口
	local  net.IP           // 发送IP数据包的源地址
	remote *net.IPAddr      // 发送IP数据包的目的地址
	pc     *ipv4.PacketConn // VRRP数据包 发送连接
	buffer []byte           // 接收数据包的缓冲区
}

// WriteMessage 发送VRRP数据包
func (conn *IPv4VRRPMsgCon) WriteMessage(packet *VRRPPacket) error {
	//cm := &ipv4.ControlMessage{TTL: 255, Src: conn.local, IfIndex: conn.itf.Index}
	if _, err := conn.pc.WriteTo(packet.ToBytes(), nil, conn.remote); err != nil {
		return fmt.Errorf("IPv4VRRPMsgCon.WriteMessage: %v", err)
	}
	return nil
}

// ReadMessage 读取VRRP数据包
func (conn *IPv4VRRPMsgCon) ReadMessage() (*VRRPPacket, error) {
	// 此处读取到的数据为 IP数据包
	var n, cm, _, err = conn.pc.ReadFrom(conn.buffer)
	if err != nil {
		return nil, fmt.Errorf("IPv4VRRPMsgCon.ReadMessage: %v", err)
	}
	// 检查 TTL 应该为 255 (see RFC5798 5.1.1.3. TTL)
	if cm.TTL != 255 {
		return nil, fmt.Errorf("IPv4VRRPMsgCon.ReadMessage: the TTL of IP datagram carring VRRP advertisment must equal to 255")
	}
	// 解析VRRP报文
	advertisement, err := FromBytes(IPv4, conn.buffer[:n])
	if err != nil {
		return nil, fmt.Errorf("IPv4VRRPMsgCon.ReadMessage: %v", err)
	}

	if advertisement.GetVersion() != byte(VRRPv3) {
		return nil, fmt.Errorf("IPv4VRRPMsgCon.ReadMessage: received an advertisement with %s", VRRPVersion(advertisement.GetVersion()))
	}

	// 构造伪首部
	var pshdr PseudoHeader
	pshdr.Saddr = cm.Src
	pshdr.Daddr = cm.Dst
	pshdr.Protocol = VRRPIPProtocolNumber
	pshdr.Len = uint16(n)
	// 校验校验码
	if !advertisement.ValidateCheckSum(&pshdr) {
		return nil, fmt.Errorf("IPv4VRRPMsgCon.ReadMessage: validate the check sum of advertisement failed, Src: %s, Dst: %s, TTL: %d", cm.Src, cm.Dst, cm.TTL)
	}

	advertisement.Pshdr = &pshdr
	return advertisement, nil
}

func (conn *IPv4VRRPMsgCon) Close() error {
	if conn.pc != nil {
		_ = conn.pc.LeaveGroup(conn.itf, conn.remote)
		return conn.pc.Close()
	}
	return nil
}

// NewIPv6VRRPMsgCon 创建的IPv6 VRRP虚拟连接
func NewIPv6VRRPMsgCon(itf *net.Interface, src, dst net.IP) (VRRPMsgConnection, error) {
	multiAddr := &net.IPAddr{IP: dst}
	conn, err := net.ListenIP("ip6:112", &net.IPAddr{})
	if err != nil {
		return nil, fmt.Errorf("NewIPv6VRRPMsgCon interface %s ip packet listen err, %v", itf.Name, err)
	}

	pc := ipv6.NewPacketConn(conn)
	_ = pc.LeaveGroup(itf, multiAddr)
	if err = pc.JoinGroup(itf, multiAddr); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("NewIPv6VRRPMsgCon interface %s join multicast group err, %v", itf.Name, err)
	}

	// 设置组播回环
	_ = pc.SetMulticastLoopback(true)
	// 设置消息的TTL为255 RFC 5798 5.1.2.3.  Hop Limit
	_ = pc.SetMulticastHopLimit(255)
	_ = pc.SetMulticastInterface(itf)
	_ = pc.SetControlMessage(ipv6.FlagHopLimit|ipv6.FlagSrc|ipv6.FlagDst|ipv6.FlagInterface, true)

	_ = conn.SetReadBuffer(2048)
	_ = conn.SetWriteBuffer(2048)

	return &IPv6VRRPMsgCon{
		buffer: make([]byte, 4096),
		local:  src,
		remote: multiAddr,
		pc:     pc,
	}, nil
}

// IPv6VRRPMsgCon IPv6的VRRP消息组播连接
type IPv6VRRPMsgCon struct {
	itf    *net.Interface   // 组播接口
	buffer []byte           // 接收数据包的缓冲区
	local  net.IP           // 发送IP数据包的源地址
	remote *net.IPAddr      // 组播地址
	pc     *ipv6.PacketConn // 组播连接
}

// WriteMessage 发送VRRP数据包
func (con *IPv6VRRPMsgCon) WriteMessage(packet *VRRPPacket) error {
	//cm := &ipv6.ControlMessage{TTL: 255, IfIndex: con.itf.Index}
	if _, err := con.pc.WriteTo(packet.ToBytes(), nil, con.remote); err != nil {
		return fmt.Errorf("IPv6VRRPMsgCon.WriteMessage: %v", err)
	}
	return nil
}

// ReadMessage 读取VRRP数据包
func (con *IPv6VRRPMsgCon) ReadMessage() (*VRRPPacket, error) {
	n, cm, _, err := con.pc.ReadFrom(con.buffer)
	if err != nil {
		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: %v", err)
	}
	// 检查 TTL 应该为 255 (see RFC5798
	if cm.HopLimit != 255 {
		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: the TTL of IP datagram carring VRRP advertisment must equal to 255")
	}

	var pshdr = PseudoHeader{
		Daddr:    cm.Src,
		Saddr:    cm.Dst,
		Protocol: VRRPIPProtocolNumber,
		Len:      uint16(n),
	}
	advertisement, err := FromBytes(IPv6, con.buffer)
	if err != nil {
		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: %v", err)
	}

	if VRRPVersion(advertisement.GetVersion()) != VRRPv3 {
		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: invalid VRRP version %v", advertisement.GetVersion())
	}
	if !advertisement.ValidateCheckSum(&pshdr) {
		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: invalid check sum")
	}
	advertisement.Pshdr = &pshdr
	return advertisement, nil
}

func (con *IPv6VRRPMsgCon) Close() error {
	if con.pc != nil {
		_ = con.pc.LeaveGroup(con.itf, con.remote)
		return con.pc.Close()
	}
	return nil
}
