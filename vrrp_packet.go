package govrrp

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"unsafe"
)

// RFC 5798 5.1. VRRP Packet Format
//
//      0                   1                   2                   3
//     0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//    +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//    |                    IPv4 Fields or IPv6 Fields                 |
//   ...                                                             ...
//    |                                                               |
//    +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//    |Version| Type  | Virtual Rtr ID|   Priority    |Count IPvX Addr|
//    +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//    |(rsvd) |     Max Adver Int     |          Checksum             |
//    +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//    |                                                               |
//    +                                                               +
//    |                       IPvX Address(es)                        |
//    +                                                               +
//    +                                                               +
//    +                                                               +
//    +                                                               +
//    |                                                               |
//    +                                                               +
//    |                                                               |
//    +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//

// VRRPPacket VRRP数据包
type VRRPPacket struct {
	Header    [8]byte       // 头部
	IPAddress [][4]byte     // 报文中IP地址序列
	Pshdr     *PseudoHeader // 伪头部，用于记录IP层信息
}

func (packet *VRRPPacket) String() string {
	version := IPv4
	if packet.Pshdr.Saddr.To4() == nil {
		version = IPv6
	}
	return fmt.Sprintf(
		"Version %d Virtual Rtr ID: %d Priority: %d Addr Count: %d Checksum: %02X IP Addresses: %+v",
		packet.GetVersion(), packet.GetVirtualRouterID(), packet.GetPriority(), packet.GetIPvXAddrCount(), packet.GetCheckSum(),
		packet.GetIPvXAddr(byte(version)))
}

// PseudoHeader 伪头部，用于记录IP层协议信息
type PseudoHeader struct {
	Saddr    net.IP // 源地址
	Daddr    net.IP // 目的地址
	Zero     uint8
	Protocol uint8  // IP层协议号 VRRP为 112（十进制）
	Len      uint16 // VRRP报文总长
}

// ToBytes 伪头部序列化为字节序列
func (psh *PseudoHeader) ToBytes() []byte {
	var octets = make([]byte, 36)
	copy(octets, psh.Saddr)
	copy(octets[16:], psh.Daddr)
	copy(octets[32:], []byte{psh.Zero, psh.Protocol, byte(psh.Len >> 8), byte(psh.Len)})
	return octets
}

// FromBytes 解析VRRP数据包
func FromBytes(IPvXVersion byte, octets []byte) (*VRRPPacket, error) {
	if len(octets) < 8 {
		return nil, errors.New("faulty VRRP packet size")
	}
	var packet VRRPPacket
	for index := 0; index < 8; index++ {
		packet.Header[index] = octets[index]
	}

	var countofaddrs = int(packet.GetIPvXAddrCount())
	switch IPvXVersion {
	case 4:
	case 6:
		countofaddrs = countofaddrs * 4
	default:
		return nil, fmt.Errorf("faulty IPvX version %d", IPvXVersion)
	}
	// to compatible with VRRP v2 packet, ignore the auth info
	if 8+countofaddrs*4 > len(octets) {
		return nil, fmt.Errorf("The value of filed IPvXAddrCount doesn't match the length of octets")
	}
	for index := 0; index < countofaddrs; index++ {
		var addr [4]byte
		addr[0] = octets[8+4*index]
		addr[1] = octets[8+4*index+1]
		addr[2] = octets[8+4*index+2]
		addr[3] = octets[8+4*index+3]
		packet.IPAddress = append(packet.IPAddress, addr)
	}
	return &packet, nil
}

// GetIPvXAddr 获取报文中的IP
func (packet *VRRPPacket) GetIPvXAddr(version byte) (addrs []net.IP) {
	switch version {
	case 4:
		for index := range packet.IPAddress {
			addrs = append(addrs, net.IPv4(
				packet.IPAddress[index][0],
				packet.IPAddress[index][1],
				packet.IPAddress[index][2],
				packet.IPAddress[index][3]))
		}
		return addrs
	case 6:
		for index := 0; index < int(packet.GetIPvXAddrCount()); index++ {
			var p = make(net.IP, net.IPv6len)
			for i := 0; i < 4; i++ {
				copy(p[4*i:], packet.IPAddress[index*4+i][:])
			}
			addrs = append(addrs, p)
		}
		return addrs
	default:
		return nil
	}
}

// AddIPvXAddr 向报文中追加IP
func (packet *VRRPPacket) AddIPvXAddr(version byte, ip net.IP) {
	switch version {
	case 4:
		ip = ip.To4()
		packet.IPAddress = append(packet.IPAddress, [4]byte{ip[0], ip[1], ip[2], ip[3]})
		packet.setIPvXAddrCount(packet.GetIPvXAddrCount() + 1)
	case 6:
		for index := 0; index < 4; index++ {
			packet.IPAddress = append(packet.IPAddress, [4]byte{ip[index*4+0], ip[index*4+1], ip[index*4+2], ip[index*4+3]})
		}
		packet.setIPvXAddrCount(packet.GetIPvXAddrCount() + 1)
	default:
	}
}

// AddIPAddr 向报文中追加IP
func (packet *VRRPPacket) AddIPAddr(ip netip.Addr) {
	if ip.Is4() {
		packet.IPAddress = append(packet.IPAddress, ip.As4())
		packet.setIPvXAddrCount(packet.GetIPvXAddrCount() + 1)
	} else if ip.Is6() {
		a16 := ip.As16()
		for index := 0; index < 4; index++ {
			packet.IPAddress = append(packet.IPAddress, [4]byte{
				a16[index*4+0], a16[index*4+1], a16[index*4+2], a16[index*4+3],
			})
		}
		packet.setIPvXAddrCount(packet.GetIPvXAddrCount() + 1)
	}
}

// GetVersion 获取 VRRP协议版本号
func (packet *VRRPPacket) GetVersion() byte {
	return (packet.Header[0] & 0xF0) >> 4
}

// SetVersion 设置 VRRP协议版本号
func (packet *VRRPPacket) SetVersion(Version VRRPVersion) {
	packet.Header[0] = (byte(Version) << 4) | (packet.Header[0] & 0x0F)
}

// GetType 获取 VRRP数据包的类型
func (packet *VRRPPacket) GetType() byte {
	return packet.Header[0] & 0x0F
}

// SetType 设置 VRRP数据包的类型，固定值 1 ADVERTISEMENT
func (packet *VRRPPacket) SetType() {
	packet.Header[0] = (packet.Header[0] & 0xF0) | 1
}

// GetVirtualRouterID 获取 虚拟路由ID
func (packet *VRRPPacket) GetVirtualRouterID() byte {
	return packet.Header[1]
}

// SetVirtualRouterID 设置 虚拟路由ID
func (packet *VRRPPacket) SetVirtualRouterID(VirtualRouterID byte) {
	packet.Header[1] = VirtualRouterID
}

// GetPriority 获取 优先级
func (packet *VRRPPacket) GetPriority() byte {
	return packet.Header[2]
}

// SetPriority 设置 优先级 0~255 最高优先
//
// 拥有与虚拟路由器关联的IPvX地址的VRRP路由器的优先级值必须为255（十进制）。
//
// 备份虚拟路由器的VRRP路由器必须使用1-254（十进制）之间的优先级值。备份虚拟路由器的VRRP路由器的默认优先级为 100 （十进制） 。
//
// 优先级值0具有特殊意义，表示当前主机已停止参与VRRP。这用于触发备份路由器快速过渡到主路由器，而无需等待当前主路由器超时。
func (packet *VRRPPacket) SetPriority(Priority byte) {
	packet.Header[2] = Priority
}

// GetIPvXAddrCount 获取 报文中的IP数量，至少为1
func (packet *VRRPPacket) GetIPvXAddrCount() byte {
	return packet.Header[3]
}

func (packet *VRRPPacket) setIPvXAddrCount(count byte) {
	packet.Header[3] = count
}

// GetAdvertisementInterval 获取 最大播发间隔
// 12-bit的字段，用于表示2条VRRP消息发送的间隔时间，单位为 厘秒， 100 厘秒 = 1 秒。
func (packet *VRRPPacket) GetAdvertisementInterval() uint16 {
	return uint16(packet.Header[4]&0x0F)<<8 | uint16(packet.Header[5])
}

// SetAdvertisementInterval 设置 最大播发间隔，单位厘秒， 100 厘秒 = 1 秒。
func (packet *VRRPPacket) SetAdvertisementInterval(interval uint16) {
	packet.Header[4] = (packet.Header[4] & 0xF0) | byte((interval>>8)&0x0F)
	packet.Header[5] = byte(interval)
}

// GetCheckSum 获取 校验和
// 用于检测VRRP消息中的数据损坏。
func (packet *VRRPPacket) GetCheckSum() uint16 {
	return uint16(packet.Header[6])<<8 | uint16(packet.Header[7])
}

// SetCheckSum 设置 校验和
// 校验和需要 伪头部 与 报文内容 进行计算，计算方式见 RFC1071
//
// pshdr: 伪头部
func (packet *VRRPPacket) SetCheckSum(pshdr *PseudoHeader) {
	var PointerAdd = func(ptr unsafe.Pointer, bytes int) unsafe.Pointer {
		return unsafe.Pointer(uintptr(ptr) + uintptr(bytes))
	}
	var octets = pshdr.ToBytes()
	octets = append(octets, packet.ToBytes()...)
	var x = len(octets)
	var ptr = unsafe.Pointer(&octets[0])
	var sum uint32
	for x > 1 {
		sum = sum + uint32(*(*uint16)(ptr))
		ptr = PointerAdd(ptr, 2)
		x = x - 2
	}
	if x > 0 {
		sum = sum + uint32(*((*uint8)(ptr)))
	}
	for (sum >> 16) > 0 {
		sum = sum&65535 + sum>>16
	}
	sum = ^sum
	packet.Header[7] = byte(sum >> 8)
	packet.Header[6] = byte(sum)
}

// ValidateCheckSum 验证 校验和
func (packet *VRRPPacket) ValidateCheckSum(pshdr *PseudoHeader) bool {
	var PointerAdd = func(ptr unsafe.Pointer, bytes int) unsafe.Pointer {
		return unsafe.Pointer(uintptr(ptr) + uintptr(bytes))
	}
	var octets = pshdr.ToBytes()
	octets = append(octets, packet.ToBytes()...)
	var x = len(octets)
	var ptr = unsafe.Pointer(&octets[0])
	var sum uint32
	for x > 1 {
		sum = sum + uint32(*(*uint16)(ptr))
		ptr = PointerAdd(ptr, 2)
		x = x - 2
	}
	if x > 0 {
		sum = sum + uint32(*((*uint8)(ptr)))
	}
	for (sum >> 16) > 0 {
		sum = sum&65535 + sum>>16
	}
	if uint16(sum) == 65535 {
		return true
	} else {
		return false
	}
}

// ToBytes 序列化消息为字节序列
func (packet *VRRPPacket) ToBytes() []byte {
	var payload = make([]byte, 8+len(packet.IPAddress)*4)
	copy(payload, packet.Header[:])
	for index := range packet.IPAddress {
		copy(payload[8+index*4:], packet.IPAddress[index][:])
	}
	return payload
}

// PacketSize 当前报文的长度
func (packet *VRRPPacket) PacketSize() int {
	return 8 + len(packet.IPAddress)*4
}
