package govrrp

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"testing"
)

func TestVRRPPacket_FromBytes(t *testing.T) {
	raw, _ := hex.DecodeString("31f0640100640608c0a800e6")
	_, err := FromBytes(IPv4, raw)
	if err != nil {
		t.Error(err)
	}
}

func TestVRRPPacket_ToBytes(t *testing.T) {
	var packet VRRPPacket
	packet.SetPriority(100)
	packet.SetVersion(VRRPv3)
	packet.SetVirtualRouterID(240)
	packet.SetAdvertisementInterval(100)
	packet.SetType()
	addr, _ := netip.ParseAddr("192.168.0.230")
	packet.AddIPAddr(addr)

	pshdr := PseudoHeader{
		Daddr:    net.ParseIP("224.0.0.18"),
		Saddr:    net.ParseIP("192.168.0.220"),
		Protocol: VRRPIPProtocolNumber,
		Len:      uint16(packet.PacketSize()),
	}
	packet.SetCheckSum(&pshdr)

	fmt.Printf("%02X\n", packet.ToBytes())
}

func TestVRRPPacket_ValidateCheckSum(t *testing.T) {
	raw, _ := hex.DecodeString("31f0640100640608c0a800e6")
	p, err := FromBytes(IPv4, raw)
	if err != nil {
		t.Error(err)
	}
	pshdr := PseudoHeader{
		Daddr:    net.ParseIP("224.0.0.18"),
		Saddr:    net.ParseIP("192.168.0.220"),
		Protocol: VRRPIPProtocolNumber,
		Len:      uint16(len(raw)),
	}
	if ok := p.ValidateCheckSum(&pshdr); !ok {
		t.Error("checksum error")
	}
}

func TestVRRPPacket_SetCheckSum(t *testing.T) {
	var packet VRRPPacket
	packet.SetPriority(100)
	packet.SetVersion(VRRPv3)
	packet.SetVirtualRouterID(240)
	packet.SetAdvertisementInterval(100)
	packet.SetType()
	addr, _ := netip.ParseAddr("192.168.0.230")
	packet.AddIPAddr(addr)

	var pshdr PseudoHeader
	pshdr.Len = uint16(packet.PacketSize())
	pshdr.Protocol = VRRPIPProtocolNumber
	pshdr.Saddr = net.ParseIP("192.168.0.101")
	pshdr.Daddr = VRRPMultiAddrIPv4
	packet.SetCheckSum(&pshdr)
	fmt.Printf("Orign  Packet Check Sum: %02X\n", packet.GetCheckSum())

	b := packet.ToBytes()

	pktCopy, err := FromBytes(IPv4, b)
	if err != nil {
		t.Error(err)
	}
	fmt.Printf("Parsed Packet Check Sum: %02X\n", pktCopy.GetCheckSum())
	if pktCopy.GetCheckSum() != packet.GetCheckSum() {
		t.Error("checksum error")
	}

}
