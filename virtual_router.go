package govrrp

import (
	"fmt"
	"net"
	"net/netip"
	"time"
)

// VirtualRouter 虚拟路由器，实现了VRRP协议的状态机
type VirtualRouter struct {
	vrID     byte // 虚拟路由ID
	priority byte // 优先级

	state int // 状态机状态 INIT | MASTER | BACKUP

	preempt bool // 是否开启 抢占模式（默认开启），开启后 优先级为高的路由器在启动后将抢占所有低优先级路由器。
	owner   bool // 是否是主节点（是否是IP的拥有者）

	virtualRouterMACAddressIPv4 net.HardwareAddr // IPv4 虚拟MAC地址
	virtualRouterMACAddressIPv6 net.HardwareAddr // IPv6 虚拟MAC地址

	advertisementInterval         uint16 // VRRP消息发送间隔时间（心跳间隔）
	advertisementIntervalOfMaster uint16 // 主节点发出VRRP消息的间隔时间（心跳间隔）
	skewTime                      uint16 // Skew_Time 用于根据节点的优先级计算 masterDownInterval
	masterDownInterval            uint16 // 主节点失效时间，主节点在该时间内未发出VRRP消息则认为主节点失效

	ift               *net.Interface      // 工作网口接口
	ipvX              byte                // IP协议类型(IPv4 或 IPv6)
	preferredSourceIP net.IP              // 优先使用的源IP地址（工作网口接口的IP地址）
	protectedIPaddrs  map[netip.Addr]bool // 虚拟IP地址集合

	vrrpConn      VRRPMsgConnection // VRRP数据包收发送接口，用于发送和接收VRRP数据包。
	addrAnnouncer AddrAnnouncer     // 虚拟IP地址广播器，用于向其他主机广播虚拟IP地址。

	eventChannel chan EVENT       // 事件通道
	packetQueue  chan *VRRPPacket // VRRP数据包队列

	advertisementTicker *time.Ticker // VRRP消息发送定时器
	masterDownTimer     *time.Timer  // 主节点失效倒计时

	transitionHandler map[transition]func()
}

// NewVirtualRouter 创建虚拟路由器
// VRID: 虚拟路由ID (0~255)
// nif: 工作网口接口名称
// Owner: 是否为MASTER
// IPvX: IP协议类型(IPv4 或 IPv6)
func NewVirtualRouter(VRID byte, nif string, Owner bool, IPvX byte) (*VirtualRouter, error) {
	if IPvX != IPv4 && IPvX != IPv6 {
		return nil, fmt.Errorf("NewVirtualRouter: parameter IPvx must be IPv4 or IPv6")
	}

	ift, err := net.InterfaceByName(nif)
	if err != nil {
		return nil, err
	}

	// 找到网口的IP地址
	preferred, err := interfacePreferIP(ift, IPvX)
	if err != nil {
		return nil, err
	}

	vr := new(VirtualRouter)

	vr.vrID = VRID
	vr.ipvX = IPvX
	vr.ift = ift
	vr.preferredSourceIP = preferred

	// ref RFC 5798 7.3. Virtual Router MAC Address
	// - IPv4 case: 00-00-5E-00-01-{VRID}
	// - IPv6 case: 00-00-5E-00-02-{VRID}
	vr.virtualRouterMACAddressIPv4, _ = net.ParseMAC(fmt.Sprintf("00-00-5E-00-01-%X", VRID))
	vr.virtualRouterMACAddressIPv6, _ = net.ParseMAC(fmt.Sprintf("00-00-5E-00-02-%X", VRID))

	// 初始化状态机状态为 INIT
	vr.state = INIT

	vr.owner = Owner
	// default values that defined by RFC 5798
	if Owner {
		vr.priority = 255
	}
	// 开启抢占模式
	vr.preempt = defaultPreempt

	vr.SetAdvInterval(defaultAdvertisementInterval)
	vr.SetPriorityAndMasterAdvInterval(defaultPriority, defaultAdvertisementInterval)

	vr.protectedIPaddrs = make(map[netip.Addr]bool)
	vr.eventChannel = make(chan EVENT, EVENT_CHANNEL_SIZE)
	vr.packetQueue = make(chan *VRRPPacket, PACKET_QUEUE_SIZE)
	vr.transitionHandler = make(map[transition]func())

	if IPvX == IPv4 {
		// 创建 IPv4 虚拟IP地址广播器
		vr.addrAnnouncer, err = NewIPv4AddrAnnouncer(ift)
		if err != nil {
			return nil, err
		}
		// 创建IPv4接口 (组播)
		vr.vrrpConn, err = NewIPv4VRRPMsgConn(ift, vr.preferredSourceIP, VRRPMultiAddrIPv4)
		if err != nil {
			return nil, err
		}
	} else {
		// 创建 IPv6 虚拟IP地址广播器
		vr.addrAnnouncer, err = NewIPIPv6AddrAnnouncer(ift)
		if err != nil {
			return nil, err
		}
		// 创建IPv6接口 (组播)
		vr.vrrpConn, err = NewIPv6VRRPMsgCon(ift, vr.preferredSourceIP, VRRPMultiAddrIPv6)
		if err != nil {
			return nil, err
		}
	}
	logger.Printf(INFO, "virtual router %v initialized, working on %v", VRID, nif)
	return vr, nil
}

// 设置 虚拟路由的优先级，如为主节点那么忽略
func (r *VirtualRouter) setPriority(Priority byte) *VirtualRouter {
	if r.owner {
		return r
	}
	r.priority = Priority
	return r
}

// SetAdvInterval 设置 VRRP消息发送间隔（心跳间隔），时间间隔不能小于 10 ms。
func (r *VirtualRouter) SetAdvInterval(Interval time.Duration) *VirtualRouter {
	if Interval < 10*time.Millisecond {
		// logger.Printf(INFO, "interval can less than 10 ms")
		Interval = 10 * time.Millisecond
	}
	r.advertisementInterval = uint16(Interval / (10 * time.Millisecond))
	return r
}

// SetPriorityAndMasterAdvInterval 设置 当前虚拟路由优先级 以及 心跳发送间隔
func (r *VirtualRouter) SetPriorityAndMasterAdvInterval(priority byte, interval time.Duration) *VirtualRouter {
	r.setPriority(priority)
	if interval < 10*time.Millisecond {
		//panic("interval can not less than 10 ms")
		interval = 10 * time.Millisecond
	}
	r.setMasterAdvInterval(uint16(interval / (10 * time.Millisecond)))
	return r
}

// 设置 主节点的心跳发送间隔时间
// 并更新 skewTime 和 masterDownInterval
func (r *VirtualRouter) setMasterAdvInterval(Interval uint16) *VirtualRouter {
	r.advertisementIntervalOfMaster = Interval

	// Skew_Time = (((256 - priority) * Master_Adver_Interval) / 256)
	// Skew_Time =  (256 * Master_Adver_Interval - priority * Master_Adver_Interval) / 256
	// Skew_Time =  Master_Adver_Interval - priority * Master_Adver_Interval / 256
	r.skewTime = r.advertisementIntervalOfMaster - uint16(float32(r.advertisementIntervalOfMaster)*float32(r.priority)/256)

	// Master_Down_Interval  = (3 * Master_Adver_Interval) + Skew_time
	r.masterDownInterval = 3*r.advertisementIntervalOfMaster + r.skewTime
	// 从 MasterDownInterval 和 SkewTime 的计算方式来看，
	// 同一组VirtualRouter中，Priority 越高的Router越快地认为某个Master失效
	return r
}

// SetPreemptMode 设置 抢占模式
// flag 为 true 时，表示开启抢占模式
func (r *VirtualRouter) SetPreemptMode(flag bool) *VirtualRouter {
	r.preempt = flag
	return r
}

func (r *VirtualRouter) AddIPvXAddr(ip net.IP) {
	key, _ := netip.AddrFromSlice(ip)
	if _, ok := r.protectedIPaddrs[key]; ok {
		logger.Printf(INFO, "VirtualRouter.AddIPvXAddr: add redundant IP addr %v", ip)
	} else {
		r.protectedIPaddrs[key] = true
	}
}

// RemoveIPvXAddr 移除 虚拟路由的虚拟IP地址
func (r *VirtualRouter) RemoveIPvXAddr(ip net.IP) {
	key, _ := netip.AddrFromSlice(ip)
	if _, ok := r.protectedIPaddrs[key]; ok {
		delete(r.protectedIPaddrs, key)
		logger.Printf(INFO, "IP %v removed", ip)
	} else {
		logger.Printf(ERROR, "VirtualRouter.RemoveIPvXAddr: remove inexistent IP addr %v", ip)
	}
}

func (r *VirtualRouter) sendAdvertMessage() {
	for k := range r.protectedIPaddrs {
		logger.Printf(DEBUG, "send advert message of IP %s", k.String())
	}
	var x = r.assembleVRRPPacket()
	if errOfWrite := r.vrrpConn.WriteMessage(x); errOfWrite != nil {
		logger.Printf(ERROR, "VirtualRouter.WriteMessage: %v", errOfWrite)
	}
}

// assembleVRRPPacket assemble VRRP advert packet
func (r *VirtualRouter) assembleVRRPPacket() *VRRPPacket {

	var packet VRRPPacket
	packet.SetPriority(r.priority)
	packet.SetVersion(VRRPv3)
	packet.SetVirtualRouterID(r.vrID)
	packet.SetAdvertisementInterval(r.advertisementInterval)
	packet.SetType()
	for k := range r.protectedIPaddrs {
		packet.AddIPAddr(k)
	}
	var pshdr PseudoHeader
	pshdr.Protocol = VRRPIPProtocolNumber
	if r.ipvX == IPv4 {
		pshdr.Daddr = VRRPMultiAddrIPv4
	} else {
		pshdr.Daddr = VRRPMultiAddrIPv6
	}
	pshdr.Len = uint16(len(packet.ToBytes()))
	pshdr.Saddr = r.preferredSourceIP
	packet.SetCheckSum(&pshdr)
	return &packet
}

// fetchVRRPPacket read VRRP packet from IP layer then push into Packet queue
func (r *VirtualRouter) fetchVRRPPacket() {
	for {
		if packet, errofFetch := r.vrrpConn.ReadMessage(); errofFetch != nil {
			logger.Printf(ERROR, "VirtualRouter.fetchVRRPPacket: %v", errofFetch)
		} else {
			if r.vrID == packet.GetVirtualRouterID() {
				r.packetQueue <- packet
			} else {
				logger.Printf(ERROR, "VirtualRouter.fetchVRRPPacket: received a advertisement with different ID: %v", packet.GetVirtualRouterID())
			}

		}
		logger.Printf(DEBUG, "VirtualRouter.fetchVRRPPacket: received one advertisement")
	}
}

func (r *VirtualRouter) makeAdvertTicker() {
	r.advertisementTicker = time.NewTicker(time.Duration(r.advertisementInterval*10) * time.Millisecond)
}

func (r *VirtualRouter) stopAdvertTicker() {
	r.advertisementTicker.Stop()
}

func (r *VirtualRouter) makeMasterDownTimer() {
	if r.masterDownTimer == nil {
		r.masterDownTimer = time.NewTimer(time.Duration(r.masterDownInterval*10) * time.Millisecond)
	} else {
		r.resetMasterDownTimer()
	}
}

func (r *VirtualRouter) stopMasterDownTimer() {
	logger.Printf(DEBUG, "master down timer stopped")
	if !r.masterDownTimer.Stop() {
		select {
		case <-r.masterDownTimer.C:
		default:
		}
		logger.Printf(DEBUG, "master down timer expired before we stop it, drain the channel")
	}
}

func (r *VirtualRouter) resetMasterDownTimer() {
	r.stopMasterDownTimer()
	r.masterDownTimer.Reset(time.Duration(r.masterDownInterval*10) * time.Millisecond)
}

func (r *VirtualRouter) resetMasterDownTimerToSkewTime() {
	r.stopMasterDownTimer()
	r.masterDownTimer.Reset(time.Duration(r.skewTime*10) * time.Millisecond)
}

func (r *VirtualRouter) Enroll(transition2 transition, handler func()) bool {
	if _, ok := r.transitionHandler[transition2]; ok {
		logger.Printf(INFO, fmt.Sprintf("VirtualRouter.Enroll(): handler of transition [%s] overwrited", transition2))
		r.transitionHandler[transition2] = handler
		return true
	}
	logger.Printf(INFO, fmt.Sprintf("VirtualRouter.Enroll(): handler of transition [%s] enrolled", transition2))
	r.transitionHandler[transition2] = handler
	return false
}

func (r *VirtualRouter) transitionDoWork(t transition) {
	var work, ok = r.transitionHandler[t]
	if ok == false {
		//return fmt.Errorf("VirtualRouter.transitionDoWork(): handler of [%s] does not exist", t)
		return
	}
	work()
	logger.Printf(INFO, fmt.Sprintf("handler of transition [%s] called", t))
	return
}

// largerThan ip1 > ip2
func largerThan(ip1, ip2 net.IP) bool {
	if len(ip1) != len(ip2) {
		logger.Printf(FATAL, "largerThan: two compared IP addresses must have the same length")
	}
	for index := range ip1 {
		if ip1[index] > ip2[index] {
			return true
		} else if ip1[index] < ip2[index] {
			return false
		}
	}
	return false
}

// eventLoop VRRP event loop to handle various triggered events
//
// RFC 5798 6.3. State Transition Diagram
//
//	                   +---------------+
//	        +--------->|               |<-------------+
//	        |          |  Initialize   |              |
//	        |   +------|               |----------+   |
//	        |   |      +---------------+          |   |
//	        |   |                                 |   |
//	        |   V                                 V   |
//	+---------------+                       +---------------+
//	|               |---------------------->|               |
//	|    Master     |                       |    Backup     |
//	|               |<----------------------|               |
//	+---------------+                       +---------------+
func (r *VirtualRouter) eventLoop() {
	for {
		switch r.state {
		case INIT:
			select {
			case event := <-r.eventChannel:
				if event == START {
					logger.Printf(INFO, "event %v received", event)
					if r.priority == 255 || r.owner {
						logger.Printf(INFO, "enter owner mode")
						r.sendAdvertMessage()
						if errOfarp := r.addrAnnouncer.AnnounceAll(r); errOfarp != nil {
							logger.Printf(ERROR, "VirtualRouter.EventLoop: %v", errOfarp)
						}
						//set up advertisement timer
						r.makeAdvertTicker()
						logger.Printf(DEBUG, "enter MASTER state")
						r.state = MASTER
						r.transitionDoWork(Init2Master)
					} else {
						logger.Printf(INFO, "VR is not the owner of protected IP addresses")
						r.setMasterAdvInterval(r.advertisementInterval)
						//set up master down timer
						r.makeMasterDownTimer()
						logger.Printf(DEBUG, "enter BACKUP state")
						r.state = BACKUP
						r.transitionDoWork(Init2Backup)
					}
				}
			}
		case MASTER:
			//check if shutdown event received
			select {
			case event := <-r.eventChannel:
				if event == SHUTDOWN {
					//close advert timer
					r.stopAdvertTicker()
					//send advertisement with priority 0
					var priority = r.priority
					r.setPriority(0)
					r.sendAdvertMessage()
					r.setPriority(priority)
					//transition into INIT
					r.state = INIT
					r.transitionDoWork(Master2Init)
					logger.Printf(INFO, "event %v received", event)
					//maybe we can break out the event loop
				}
			case <-r.advertisementTicker.C: //check if advertisement timer fired
				r.sendAdvertMessage()
			default:
				//nothing to do, just break
			}
			//process incoming advertisement
			select {
			case packet := <-r.packetQueue:
				if packet.GetPriority() == 0 {
					//I don't think we should anything here
				} else {
					if packet.GetPriority() > r.priority || (packet.GetPriority() == r.priority && largerThan(packet.Pshdr.Saddr, r.preferredSourceIP)) {

						//cancel Advertisement timer
						r.stopAdvertTicker()
						//set up master down timer
						r.setMasterAdvInterval(packet.GetAdvertisementInterval())
						r.makeMasterDownTimer()
						r.state = BACKUP
						r.transitionDoWork(Master2Backup)
					} else {
						//just discard this one
					}
				}
			default:
				//nothing to do
			}
		case BACKUP:
			select {
			case event := <-r.eventChannel:
				if event == SHUTDOWN {
					//close master down timer
					r.stopMasterDownTimer()
					//transition into INIT
					r.state = INIT
					r.transitionDoWork(Backup2Init)
					logger.Printf(INFO, "event %s received", event)
				}
			default:
			}
			//process incoming advertisement
			select {
			case packet := <-r.packetQueue:
				if packet.GetPriority() == 0 {
					logger.Printf(INFO, "received an advertisement with priority 0, transit into MASTER state", r.vrID)
					//Set the Master_Down_Timer to Skew_Time
					r.resetMasterDownTimerToSkewTime()
				} else {
					if r.preempt == false || packet.GetPriority() > r.priority || (packet.GetPriority() == r.priority && largerThan(packet.Pshdr.Saddr, r.preferredSourceIP)) {
						//reset master down timer
						r.setMasterAdvInterval(packet.GetAdvertisementInterval())
						r.resetMasterDownTimer()
					} else {
						//nothing to do, just discard this one
					}
				}
			default:
				//nothing to do
			}
			select {
			//Master_Down_Timer fired
			case <-r.masterDownTimer.C:
				// Send an ADVERTISEMENT
				r.sendAdvertMessage()
				if errOfARP := r.addrAnnouncer.AnnounceAll(r); errOfARP != nil {
					logger.Printf(ERROR, "VirtualRouter.EventLoop: %v", errOfARP)
				}
				//Set the Advertisement Timer to Advertisement interval
				r.makeAdvertTicker()

				r.state = MASTER
				r.transitionDoWork(Backup2Master)
			default:
				//nothing to do
			}

		}
	}
}

// eventSelector VRRP event selector to handle various triggered events
func (r *VirtualRouter) eventSelector() {
	for {
		switch r.state {
		case INIT:
			select {
			case event := <-r.eventChannel:
				if event == START {
					logger.Printf(INFO, "event %v received", event)
					if r.priority == 255 || r.owner {
						logger.Printf(INFO, "enter owner mode")
						r.sendAdvertMessage()
						if errOfarp := r.addrAnnouncer.AnnounceAll(r); errOfarp != nil {
							logger.Printf(ERROR, "VirtualRouter.EventLoop: %v", errOfarp)
						}
						//set up advertisement timer
						r.makeAdvertTicker()

						logger.Printf(DEBUG, "enter MASTER state")
						r.state = MASTER
						r.transitionDoWork(Init2Master)
					} else {
						logger.Printf(INFO, "VR is not the owner of protected IP addresses")
						r.setMasterAdvInterval(r.advertisementInterval)
						//set up master down timer
						r.makeMasterDownTimer()
						logger.Printf(DEBUG, "enter BACKUP state")
						r.state = BACKUP
						r.transitionDoWork(Init2Backup)
					}
				}
			}
		case MASTER:
			//check if shutdown event received
			select {
			case event := <-r.eventChannel:
				if event == SHUTDOWN {
					//close advert timer
					r.stopAdvertTicker()
					//send advertisement with priority 0
					var priority = r.priority
					r.setPriority(0)
					r.sendAdvertMessage()
					r.setPriority(priority)
					//transition into INIT
					r.state = INIT
					r.transitionDoWork(Master2Init)
					logger.Printf(INFO, "event %v received", event)
					//maybe we can break out the event loop
				}
			case <-r.advertisementTicker.C: //check if advertisement timer fired
				r.sendAdvertMessage()
			case packet := <-r.packetQueue: //process incoming advertisement
				if packet.GetPriority() == 0 {
					//I don't think we should anything here
				} else {
					if packet.GetPriority() > r.priority || (packet.GetPriority() == r.priority && largerThan(packet.Pshdr.Saddr, r.preferredSourceIP)) {

						//cancel Advertisement timer
						r.stopAdvertTicker()
						//set up master down timer
						r.setMasterAdvInterval(packet.GetAdvertisementInterval())
						r.makeMasterDownTimer()
						r.state = BACKUP
						r.transitionDoWork(Master2Backup)
					} else {
						//just discard this one
					}
				}
			}

		case BACKUP:
			select {
			case event := <-r.eventChannel:
				if event == SHUTDOWN {
					//close master down timer
					r.stopMasterDownTimer()
					//transition into INIT
					r.state = INIT
					r.transitionDoWork(Backup2Init)
					logger.Printf(INFO, "event %s received", event)
				}
			case packet := <-r.packetQueue: //process incoming advertisement
				if packet.GetPriority() == 0 {
					logger.Printf(INFO, "received an advertisement with priority 0, transit into MASTER state", r.vrID)
					//Set the Master_Down_Timer to Skew_Time
					r.resetMasterDownTimerToSkewTime()
				} else {
					if r.preempt == false || packet.GetPriority() > r.priority || (packet.GetPriority() == r.priority && largerThan(packet.Pshdr.Saddr, r.preferredSourceIP)) {
						//reset master down timer
						r.setMasterAdvInterval(packet.GetAdvertisementInterval())
						r.resetMasterDownTimer()
					} else {
						//nothing to do, just discard this one
					}
				}
			case <-r.masterDownTimer.C: //Master_Down_Timer fired
				// Send an ADVERTISEMENT
				r.sendAdvertMessage()
				if errOfARP := r.addrAnnouncer.AnnounceAll(r); errOfARP != nil {
					logger.Printf(ERROR, "VirtualRouter.EventLoop: %v", errOfARP)
				}
				//Set the Advertisement Timer to Advertisement interval
				r.makeAdvertTicker()
				r.state = MASTER
				r.transitionDoWork(Backup2Master)
			}

		}
	}
}

func (r *VirtualRouter) StartWithEventLoop() {
	go r.fetchVRRPPacket()
	go func() {
		r.eventChannel <- START
	}()
	r.eventLoop()
}

func (r *VirtualRouter) StartWithEventSelector() {
	go r.fetchVRRPPacket()
	go func() {
		r.eventChannel <- START
	}()
	r.eventSelector()
}

func (r *VirtualRouter) Stop() {
	r.eventChannel <- SHUTDOWN
}
