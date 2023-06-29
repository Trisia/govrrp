package govrrp

import (
	"fmt"
	"github.com/mdlayher/arp"
	"github.com/mdlayher/ndp"
	"io"
	"net"
	"time"
)

type AddrAnnouncer interface {
	io.Closer
	AnnounceAll(vr *VirtualRouter) error
}

// IPv6AddrAnnouncer IPv6 NDP广播，在指定网口上广播NDP消息通知其他主机VIP地址
type IPv6AddrAnnouncer struct {
	con *ndp.Conn
}

// NewIPIPv6AddrAnnouncer 创建IPv6 NDP广播
func NewIPIPv6AddrAnnouncer(nif *net.Interface) (*IPv6AddrAnnouncer, error) {
	con, ip, err := ndp.Listen(nif, ndp.LinkLocal)
	if err != nil {
		return nil, fmt.Errorf("IPv6AddrAnnouncer: %v", err)
	}
	logg.Printf("NDP client initialized, working on %v, source IP %v", nif.Name, ip)
	return &IPv6AddrAnnouncer{con: con}, nil
}

// AnnounceAll 广播 包含所有的IPv6虚拟IP地址
func (nd *IPv6AddrAnnouncer) AnnounceAll(vr *VirtualRouter) error {
	for key := range vr.protectedIPaddrs {
		multicastgroup, err := ndp.SolicitedNodeMulticast(key)
		if err != nil {
			// logg.Printf(ERROR, "IPv6AddrAnnouncer.AnnounceAll: %v", err)
			return err
		} else {
			//send unsolicited NeighborAdvertisement to refresh link layer address cache
			var msg = &ndp.NeighborAdvertisement{
				Override:      true,
				TargetAddress: key,
				Options: []ndp.Option{
					&ndp.LinkLayerAddress{
						Direction: ndp.Source,
						Addr:      vr.ift.HardwareAddr,
					},
				},
			}
			if err = nd.con.WriteTo(msg, nil, multicastgroup); err != nil {
				// logg.Printf(ERROR, "IPv6AddrAnnouncer.AnnounceAll: %v", err)
				return err
			} else {
				logg.Printf("send unsolicited neighbor advertisement for %s", key.String())
			}
		}
	}

	return nil
}

func (nd *IPv6AddrAnnouncer) Close() error {
	if nd != nil && nd.con != nil {
		return nd.con.Close()
	}
	return nil
}

// IPv4AddrAnnouncer IPv4 Gratuitous ARP广播，在指定网口上广播Gratuitous ARP消息通知其他主机VIP地址
type IPv4AddrAnnouncer struct {
	ARPClient *arp.Client
}

// NewIPv4AddrAnnouncer 创建IPv4 Gratuitous ARP广播
func NewIPv4AddrAnnouncer(nif *net.Interface) (*IPv4AddrAnnouncer, error) {
	if aar, err := arp.Dial(nif); err != nil {
		return nil, err
	} else {
		// logg.Printf(DEBUG, "IPv4 addresses announcer created")
		return &IPv4AddrAnnouncer{ARPClient: aar}, nil
	}
}

// AnnounceAll 广播 gratuitous ARP response 包含所有的IPv4虚拟IP地址
func (ar *IPv4AddrAnnouncer) AnnounceAll(vr *VirtualRouter) error {
	if err := ar.ARPClient.SetWriteDeadline(time.Now().Add(500 * time.Microsecond)); err != nil {
		return err
	}
	// 构造 gratuitous ARP response
	var packet arp.Packet
	packet.HardwareType = 1       // ethernet
	packet.ProtocolType = 0x0800  // IPv4 protocol
	packet.HardwareAddrLength = 6 // ethernet mac address length
	packet.IPLength = 4           // IPv4 address length
	packet.Operation = 2          // Type response

	for k := range vr.protectedIPaddrs {
		packet.SenderHardwareAddr = vr.ift.HardwareAddr
		packet.SenderIP = k
		packet.TargetHardwareAddr = BroadcastHADAR
		packet.TargetIP = k
		logg.Printf("send gratuitous arp for %s", k.String())
		if err := ar.ARPClient.WriteTo(&packet, BroadcastHADAR); err != nil {
			return fmt.Errorf("IPv4AddrAnnouncer.AnnounceAll: %v", err)
		}
	}
	return nil
}

func (ar *IPv4AddrAnnouncer) Close() error {
	if ar != nil && ar.ARPClient != nil {
		return ar.ARPClient.Close()
	}
	return nil
}
