package govrrp

import (
	"fmt"
	"net"
	"syscall"
)

// VRRPMsgConnection IP层VRRP协议消息接口
type VRRPMsgConnection interface {
	// WriteMessage 发送VRRP消息
	WriteMessage(*VRRPPacket) error
	// ReadMessage 接收VRRP消息
	ReadMessage() (*VRRPPacket, error)
}

// IPv4VRRPMsgCon IPv4的VRRP消息组播连接
type IPv4VRRPMsgCon struct {
	buffer     []byte      // 接收缓冲区
	remote     net.IP      // 发送IP数据包的目的地址
	local      net.IP      // 发送IP数据包的源地址
	SendCon    *net.IPConn // VRRP数据包 发送连接
	ReceiveCon *net.IPConn // IP数据包 接收连接
}

// IPv6VRRPMsgCon IPv6的VRRP消息组播连接
type IPv6VRRPMsgCon struct {
	buffer []byte
	oob    []byte
	remote net.IP
	local  net.IP
	Con    *net.IPConn
}

// ipConnection 创建一个IP数据包连接
// ift: 网络接口
// src: IP数据包源地址
// dst: IP数据包目的地址
func ipConnection(ift *net.Interface, src, dst net.IP) (*net.IPConn, error) {

	var conn *net.IPConn
	var err error

	if src.IsLinkLocalUnicast() {
		conn, err = net.ListenIP("ip:112", &net.IPAddr{IP: src, Zone: ift.Name})
	} else {
		conn, err = net.ListenIP("ip:112", &net.IPAddr{IP: src})
	}
	if err != nil {
		return nil, err
	}
	fd, err := conn.File()
	if err != nil {
		return nil, err
	}
	defer fd.Close()

	// 设置 Socket参数支持IP组播
	if dst.To4() != nil {
		// IPv4 mode
		// set hop limit
		if err = syscall.SetsockoptInt(int(fd.Fd()), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, VRRPMultiTTL); err != nil {
			return nil, fmt.Errorf("ipConnection: %v", err)
		}
		// set tos
		if err = syscall.SetsockoptInt(int(fd.Fd()), syscall.IPPROTO_IP, syscall.IP_TOS, 7); err != nil {
			return nil, fmt.Errorf("ipConnection: %v", err)
		}
		// disable multicast loop
		if err = syscall.SetsockoptInt(int(fd.Fd()), syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, 0); err != nil {
			return nil, fmt.Errorf("ipConnection: %v", err)
		}
	} else {
		// IPv6 mode
		// set hop limit
		if err = syscall.SetsockoptInt(int(fd.Fd()), syscall.IPPROTO_IPV6, syscall.IPV6_MULTICAST_HOPS, 255); err != nil {
			return nil, fmt.Errorf("ipConnection: %v", err)
		}
		// disable multicast loop
		if err = syscall.SetsockoptInt(int(fd.Fd()), syscall.IPPROTO_IPV6, syscall.IPV6_MULTICAST_LOOP, 0); err != nil {
			return nil, fmt.Errorf("ipConnection: %v", err)
		}
		// to receive the hop limit and dst address in oob
		if err = syscall.SetsockoptInt(int(fd.Fd()), syscall.IPPROTO_IPV6, syscall.IPV6_2292HOPLIMIT, 1); err != nil {
			return nil, fmt.Errorf("ipConnection: %v", err)
		}
		if err = syscall.SetsockoptInt(int(fd.Fd()), syscall.IPPROTO_IPV6, syscall.IPV6_2292PKTINFO, 1); err != nil {
			return nil, fmt.Errorf("ipConnection: %v", err)
		}
	}
	logger.Printf(INFO, "IP virtual connection established %v ==> %v", src, dst)
	return conn, nil
}

// makeMulticastIPv4Conn 创建一个IPv4组播连接
func makeMulticastIPv4Conn(multi, local net.IP) (*net.IPConn, error) {
	conn, err := net.ListenIP("ip4:112", &net.IPAddr{IP: multi})
	if err != nil {
		return nil, fmt.Errorf("makeMulticastIPv4Conn: %v", err)
	}
	fd, err := conn.File()
	if err != nil {
		return nil, fmt.Errorf("makeMulticastIPv4Conn: %v", err)
	}
	defer fd.Close()
	multi = multi.To4()
	local = local.To4()
	var mreq = &syscall.IPMreq{
		Multiaddr: [4]byte{multi[0], multi[1], multi[2], multi[3]},
		Interface: [4]byte{local[0], local[1], local[2], local[3]},
	}
	if err = syscall.SetsockoptIPMreq(int(fd.Fd()), syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, mreq); err != nil {
		return nil, fmt.Errorf("makeMulticastIPv4Conn: %v", err)
	}
	return conn, nil
}

// makeMulticastIPv6Conn 加入一个IPv6组播连接
func joinIPv6MulticastGroup(ift *net.Interface, con *net.IPConn, local, remote net.IP) error {
	var fd, errOfGetFD = con.File()
	if errOfGetFD != nil {
		return fmt.Errorf("joinIPv6MulticastGroup: %v", errOfGetFD)
	}
	defer fd.Close()
	var mreq = &syscall.IPv6Mreq{}
	copy(mreq.Multiaddr[:], remote.To16())
	mreq.Interface = uint32(ift.Index)
	if errOfSetMreq := syscall.SetsockoptIPv6Mreq(int(fd.Fd()), syscall.IPPROTO_IPV6, syscall.IPV6_JOIN_GROUP, mreq); errOfSetMreq != nil {
		return fmt.Errorf("joinIPv6MulticastGroup: %v", errOfSetMreq)
	}
	logger.Printf(INFO, "Join IPv6 multicast group %v on %v", remote, ift.Name)
	return nil
}

// NewIPv4VRRPMsgConn 创建的IPv4 VRRP虚拟连接
// ift: 工作网口
// src: IP数据包中源地址，应该为工作网口的IP地址
// dst: IP数据包中目的地址，应该为组播地址 VRRPMultiAddrIPv4
func NewIPv4VRRPMsgConn(ift *net.Interface, src, dst net.IP) (VRRPMsgConnection, error) {
	sendConn, err := ipConnection(ift, src, dst)
	if err != nil {
		return nil, err
	}
	receiveConn, err := makeMulticastIPv4Conn(VRRPMultiAddrIPv4, src)
	if err != nil {
		return nil, err
	}
	return &IPv4VRRPMsgCon{
		buffer:     make([]byte, 2048),
		local:      src,
		remote:     dst,
		SendCon:    sendConn,
		ReceiveCon: receiveConn,
	}, nil
}

// WriteMessage 发送VRRP数据包
func (conn *IPv4VRRPMsgCon) WriteMessage(packet *VRRPPacket) error {
	if _, err := conn.SendCon.WriteTo(packet.ToBytes(), &net.IPAddr{IP: conn.remote}); err != nil {
		return fmt.Errorf("IPv4VRRPMsgCon.WriteMessage: %v", err)
	}
	return nil
}

// ReadMessage 读取VRRP数据包
func (conn *IPv4VRRPMsgCon) ReadMessage() (*VRRPPacket, error) {
	// 此处读取到的数据为 IP数据包
	var n, err = conn.ReceiveCon.Read(conn.buffer)
	if err != nil {
		return nil, fmt.Errorf("IPv4VRRPMsgCon.ReadMessage: %v", err)
	}
	// IP数据包长度必须大于20字节
	if n < 20 {
		return nil, fmt.Errorf("IPv4VRRPMsgCon.ReadMessage: IP datagram lenght %v too small", n)
	}
	// 获取 IPv4 头部长度
	var hdrlen = (int(conn.buffer[0]) & 0x0F) << 2
	if hdrlen > n {
		return nil, fmt.Errorf("IPv4VRRPMsgCon.ReadMessage: the header length %v is lagger than total length %d", hdrlen, n)
	}
	// 检查 TTL 应该为 255 (see RFC5798 5.1.1.3. TTL)
	if conn.buffer[8] != 255 {
		return nil, fmt.Errorf("IPv4VRRPMsgCon.ReadMessage: the TTL of IP datagram carring VRRP advertisment must equal to 255")
	}
	// 解析VRRP报文
	advertisement, err := FromBytes(IPv4, conn.buffer[hdrlen:n])
	if err != nil {
		return nil, fmt.Errorf("IPv4VRRPMsgCon.ReadMessage: %v", err)
	}

	if advertisement.GetVersion() != byte(VRRPv3) {
		return nil, fmt.Errorf("IPv4VRRPMsgCon.ReadMessage: received an advertisement with %s", VRRPVersion(advertisement.GetVersion()))
	}

	// 构造伪首部
	var pshdr PseudoHeader
	pshdr.Saddr = net.IPv4(conn.buffer[12], conn.buffer[13], conn.buffer[14], conn.buffer[15]).To16()
	pshdr.Daddr = net.IPv4(conn.buffer[16], conn.buffer[17], conn.buffer[18], conn.buffer[19]).To16()
	pshdr.Protocol = VRRPIPProtocolNumber
	pshdr.Len = uint16(n - hdrlen)
	// 校验校验码
	if !advertisement.ValidateCheckSum(&pshdr) {
		return nil, fmt.Errorf("IPv4VRRPMsgCon.ReadMessage: validate the check sum of advertisement failed")
	}

	advertisement.Pshdr = &pshdr
	return advertisement, nil
}

// NewIPv6VRRPMsgCon 创建的IPv6 VRRP虚拟连接
func NewIPv6VRRPMsgCon(ift *net.Interface, src, dst net.IP) (*IPv6VRRPMsgCon, error) {
	con, err := ipConnection(ift, src, dst)
	if err != nil {
		return nil, fmt.Errorf("NewIPv6VRRPMsgCon: %v", err)
	}
	if err = joinIPv6MulticastGroup(ift, con, src, dst); err != nil {
		return nil, fmt.Errorf("NewIPv6VRRPMsgCon: %v", err)
	}
	return &IPv6VRRPMsgCon{
		buffer: make([]byte, 4096),
		oob:    make([]byte, 4096),
		local:  src,
		remote: dst,
		Con:    con,
	}, nil
}

// WriteMessage 发送VRRP数据包
func (con *IPv6VRRPMsgCon) WriteMessage(packet *VRRPPacket) error {
	if _, errOfWrite := con.Con.WriteToIP(packet.ToBytes(), &net.IPAddr{IP: con.remote}); errOfWrite != nil {
		return fmt.Errorf("IPv6VRRPMsgCon.WriteMessage: %v", errOfWrite)
	}
	return nil
}

// ReadMessage 读取VRRP数据包
func (con *IPv6VRRPMsgCon) ReadMessage() (*VRRPPacket, error) {
	var buffern, oobn, _, raddr, errOfRead = con.Con.ReadMsgIP(con.buffer, con.oob)
	if errOfRead != nil {
		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: %v", errOfRead)
	}
	oobdata, err := syscall.ParseSocketControlMessage(con.oob[:oobn])
	if err != nil {
		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: %v", err)
	}
	var (
		dst    net.IP
		TTL    byte
		GetTTL = false
	)
	for index := range oobdata {
		if oobdata[index].Header.Level != syscall.IPPROTO_IPV6 {
			continue
		}
		switch oobdata[index].Header.Type {
		case syscall.IPV6_2292HOPLIMIT:
			if len(oobdata[index].Data) == 0 {
				return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: invalid HOPLIMIT")
			}
			TTL = oobdata[index].Data[0]
			GetTTL = true
		case syscall.IPV6_2292PKTINFO:
			if len(oobdata[index].Data) < 16 {
				return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: invalid destination IP addrress length")
			}
			dst = net.IP(oobdata[index].Data[:16])
		}
	}
	if GetTTL == false {
		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: HOPLIMIT not found")
	}
	if dst == nil {
		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: destination address not found")
	}
	var pshdr = PseudoHeader{
		Daddr:    dst,
		Saddr:    raddr.IP,
		Protocol: VRRPIPProtocolNumber,
		Len:      uint16(buffern),
	}
	advertisement, err := FromBytes(IPv6, con.buffer)
	if err != nil {
		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: %v", err)
	}
	if TTL != 255 {
		return nil, fmt.Errorf("IPv6VRRPMsgCon.ReadMessage: invalid HOPLIMIT")
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

// interfacePreferIP 获取网口上第一个IPv4或IPv6地址
func interfacePreferIP(itf *net.Interface, IPvX byte) (net.IP, error) {
	addrs, err := itf.Addrs()
	if err != nil {
		return nil, fmt.Errorf("interfacePreferIP: %v", err)
	}
	for _, addr := range addrs {
		ipaddr, _, _ := net.ParseCIDR(addr.String())
		if len(ipaddr) == 0 {
			continue
		}
		if IPvX == IPv4 {
			if ipaddr.To4() != nil {
				if ipaddr.IsGlobalUnicast() {
					return ipaddr, nil
				}
			}
		} else {
			if ipaddr.To4() == nil {
				if ipaddr.IsLinkLocalUnicast() {
					return ipaddr, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("interfacePreferIP: can not find valid IP addrs on %v", itf.Name)
}
