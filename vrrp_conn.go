package govrrp

import (
	"fmt"
	"golang.org/x/net/ipv4"
	"log"
	"net"
)

// NewIPv4VRRPMsgConn 创建的IPv4 VRRP虚拟连接
// ift: 工作网口
// src: IP数据包中源地址，应该为工作网口的IP地址
// dst: IP数据包中目的地址，应该为组播地址 VRRPMultiAddrIPv4
func NewIPv4VRRPMsgConn(itf *net.Interface, src, dst net.IP) (VRRPMsgConnection, error) {
	multiAddr := &net.IPAddr{IP: VRRPMultiAddrIPv4}

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
	itf    *net.Interface
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
		log.Println("Src:", cm.Src, "Dst:", cm.Dst, "Protocol:", pshdr.Protocol, "Len:", pshdr.Len, "TTL:", cm.TTL)
		return nil, fmt.Errorf("IPv4VRRPMsgCon.ReadMessage: validate the check sum of advertisement failed, Src: %s, Dst: %s, TTL: %d", cm.Src, cm.Dst, cm.TTL)
	}

	advertisement.Pshdr = &pshdr
	return advertisement, nil
}

func (conn *IPv4VRRPMsgCon) Close() error {
	if conn.pc != nil {
		_ = conn.pc.LeaveGroup(conn.itf, conn.remote)
		_ = conn.pc.Close()
	}
	return nil
}

//
//// NewIPv6VRRPMsgCon 创建的IPv6 VRRP虚拟连接
//func NewIPv6VRRPMsgCon(ift *net.Interface, src, dst net.IP) (VRRPMsgConnection, error) {
//	con, err := ipConnection(ift, src, dst)
//	if err != nil {
//		return nil, fmt.Errorf("NewIPv6VRRPMsgCon: %v", err)
//	}
//	if err = joinIPv6MulticastGroup(ift, con, src, dst); err != nil {
//		return nil, fmt.Errorf("NewIPv6VRRPMsgCon: %v", err)
//	}
//	return &IPv6VRRPMsgCon{
//		buffer: make([]byte, 4096),
//		oob:    make([]byte, 4096),
//		local:  src,
//		remote: dst,
//		Con:    con,
//	}, nil
//}
//
//// IPv6VRRPMsgCon IPv6的VRRP消息组播连接
//type IPv6VRRPMsgCon struct {
//	buffer []byte
//	oob    []byte
//	remote net.IP
//	local  net.IP
//	Con    *net.IPConn
//}
//
//// WriteMessage 发送VRRP数据包
//func (con *IPv6VRRPMsgCon) WriteMessage(packet *VRRPPacket) error {
//	if _, errOfWrite := con.Con.WriteToIP(packet.ToBytes(), &net.IPAddr{IP: con.remote}); errOfWrite != nil {
//		return fmt.Errorf("IPv6VRRPMsgCon.WriteMessage: %v", errOfWrite)
//	}
//	return nil
//}
//
//// ReadMessage 读取VRRP数据包
//func (con *IPv6VRRPMsgCon) ReadMessage() (*VRRPPacket, error) {
//	var buffern, oobn, _, raddr, errOfRead = con.Con.ReadMsgIP(con.buffer, con.oob)
//	if errOfRead != nil {
//		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: %v", errOfRead)
//	}
//	oobdata, err := syscall.ParseSocketControlMessage(con.oob[:oobn])
//	if err != nil {
//		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: %v", err)
//	}
//	var (
//		dst    net.IP
//		TTL    byte
//		GetTTL = false
//	)
//	for index := range oobdata {
//		if oobdata[index].Header.Level != syscall.IPPROTO_IPV6 {
//			continue
//		}
//		switch oobdata[index].Header.Type {
//		case syscall.IPV6_2292HOPLIMIT:
//			if len(oobdata[index].Data) == 0 {
//				return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: invalid HOPLIMIT")
//			}
//			TTL = oobdata[index].Data[0]
//			GetTTL = true
//		case syscall.IPV6_2292PKTINFO:
//			if len(oobdata[index].Data) < 16 {
//				return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: invalid destination IP addrress length")
//			}
//			dst = net.IP(oobdata[index].Data[:16])
//		}
//	}
//	if GetTTL == false {
//		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: HOPLIMIT not found")
//	}
//	if dst == nil {
//		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: destination address not found")
//	}
//	var pshdr = PseudoHeader{
//		Daddr:    dst,
//		Saddr:    raddr.IP,
//		Protocol: VRRPIPProtocolNumber,
//		Len:      uint16(buffern),
//	}
//	advertisement, err := FromBytes(IPv6, con.buffer)
//	if err != nil {
//		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: %v", err)
//	}
//	if TTL != 255 {
//		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: invalid HOPLIMIT")
//	}
//	if VRRPVersion(advertisement.GetVersion()) != VRRPv3 {
//		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: invalid VRRP version %v", advertisement.GetVersion())
//	}
//	if !advertisement.ValidateCheckSum(&pshdr) {
//		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: invalid check sum")
//	}
//	advertisement.Pshdr = &pshdr
//	return advertisement, nil
//}
//
//func (con *IPv6VRRPMsgCon) Close() error {
//	if con.Con != nil {
//		return con.Con.Close()
//	}
//	return nil
//}
