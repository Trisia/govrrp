package govrrp

import (
	"net"
	"time"
)

type VRRPVersion byte

const (
	VRRPv1 VRRPVersion = 1
	VRRPv2 VRRPVersion = 2
	VRRPv3 VRRPVersion = 3
)

func (v VRRPVersion) String() string {
	switch v {
	case VRRPv1:
		return "VRRP Version 1"
	case VRRPv2:
		return "VRRP Version 2"
	case VRRPv3:
		return "VRRP Version 3"
	default:
		return "unknown VRRP version"
	}
}

const (
	IPv4 byte = 4
	IPv6 byte = 6
)

const (
	INIT   uint32 = 0
	MASTER uint32 = 1
	BACKUP uint32 = 2
)

const (
	VRRPMultiTTL         = 255
	VRRPIPProtocolNumber = 112 // IANA为VRRP分配的IPv4协议号为 112（十进制）。
)

// VRRPMultiAddrIPv4 VRRP协议多播IPv4地址 （RFC5798 5.1.1.2）
// IANA为VRRP分配的IPv4多播地址为： 224.0.0.18
var VRRPMultiAddrIPv4 = net.IPv4(224, 0, 0, 18)

// VRRPMultiAddrIPv6 VRRP协议多播IPv6地址 （RFC5798 5.1.2.2）
// IANA为VRRP分配的IPv6多播地址为 FF02:0:0:0:0:0:0:12
var VRRPMultiAddrIPv6 = net.ParseIP("FF02:0:0:0:0:0:0:12")

// 广播地址
var BroadcastHADAR, _ = net.ParseMAC("ff:ff:ff:ff:ff:ff")

type EVENT byte

const (
	SHUTDOWN EVENT = 0
	START    EVENT = 1
)

func (e EVENT) String() string {
	switch e {
	case START:
		return "START"
	case SHUTDOWN:
		return "SHUTDOWN"
	default:
		return "unknown event"
	}
}

const PACKET_QUEUE_SIZE = 512
const EVENT_CHANNEL_SIZE = 1

// transition 状态切换类型
type transition int

func (t transition) String() string {
	switch t {
	case Master2Backup:
		return "master to backup"
	case Backup2Master:
		return "backup to master"
	case Init2Master:
		return "init to master"
	case Init2Backup:
		return "init to backup"
	case Backup2Init:
		return "backup to init"
	case Master2Init:
		return "master to init"
	default:
		return "unknown transition"
	}
}

const (
	Master2Backup transition = iota
	Backup2Master
	Init2Master
	Init2Backup
	Master2Init
	Backup2Init
)

const (
	defaultPriority              byte = 100
	defaultAdvertisementInterval      = 1 * time.Second
)
