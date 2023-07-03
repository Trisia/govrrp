package govrrp

import (
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"time"
)

var logg = log.New(os.Stdout, "[govrrp] ", log.LstdFlags)

// SetDefaultLogger 设置默认日志记录器
func SetDefaultLogger(l *log.Logger) {
	if l != nil {
		logg = l
	}
}

// VirtualRouter 虚拟路由器，实现了VRRP协议的状态机
type VirtualRouter struct {
	vrID     byte // 虚拟路由ID
	priority byte // 优先级

	state uint32 // 状态机状态 INIT | MASTER | BACKUP

	// preempt 抢占模式 控制虚拟路由的启动或重新启动，
	// 高优先级备份路由器是否抢占低优先级主路由器。
	// 值为 true 表示 允许抢占，值为 false 表示 禁止抢占。默认值为 true。
	preempt bool
	owner   bool // 是否是主节点（是否是IP的拥有者）

	// 为了防止与区域网内的其他VRRP路由器冲突，暂时不使用虚拟MAC地址，而是使用工作网口接口的MAC地址
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

	// 状态转换处理函数集合，用于注册用户监听的状态处理函数
	// 当状态机状态发生变化时，将调用对应的处理函数
	transitionHandler map[transition]func(*VirtualRouter)
}

// NewVirtualRouterSpec 创建一个虚拟路由器实例
// VRID: 虚拟路由ID
// ift: 工作网口接口
// preferIP: 优先使用的源IP地址，请确保工作网口配置由该IP地址保持一致。
// priority: 优先级，255 表示主节点，0 为特殊值不可使用，默认100。
func NewVirtualRouterSpec(VRID byte, ift *net.Interface, preferIP net.IP, priority byte) (*VirtualRouter, error) {
	var err error
	var ipvX byte
	if preferIP.To4() != nil {
		ipvX = IPv4
		preferIP = preferIP.To4()
	} else if preferIP.To16() != nil {
		ipvX = IPv6
		preferIP = preferIP.To16()
	} else {
		return nil, fmt.Errorf("invalid IP address")
	}

	vr := new(VirtualRouter)

	if vr.priority == 255 {
		vr.owner = true
	}
	// 初始化状态机状态为 INIT
	atomic.StoreUint32(&vr.state, INIT)
	// 开启 抢占模式
	vr.preempt = true

	vr.vrID = VRID
	vr.ipvX = ipvX
	vr.ift = ift
	vr.preferredSourceIP = preferIP

	// ref RFC 5798 7.3. Virtual Router MAC Address
	// - IPv4 case: 00-00-5E-00-01-{VRID}
	// - IPv6 case: 00-00-5E-00-02-{VRID}
	vr.virtualRouterMACAddressIPv4, _ = net.ParseMAC(fmt.Sprintf("00-00-5E-00-01-%X", VRID))
	vr.virtualRouterMACAddressIPv6, _ = net.ParseMAC(fmt.Sprintf("00-00-5E-00-02-%X", VRID))

	vr.SetAdvInterval(defaultAdvertisementInterval)
	vr.SetPriorityAndMasterAdvInterval(priority, defaultAdvertisementInterval)

	vr.protectedIPaddrs = make(map[netip.Addr]bool)
	vr.eventChannel = make(chan EVENT, EVENT_CHANNEL_SIZE)
	vr.packetQueue = make(chan *VRRPPacket, PACKET_QUEUE_SIZE)
	vr.transitionHandler = make(map[transition]func(*VirtualRouter))

	if ipvX == IPv4 {
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
	logg.Printf("VRID [%d] initialized, working on %s", VRID, ift.Name)
	return vr, nil
}

// NewVirtualRouter 创建虚拟路由器
// VRID: 虚拟路由ID (0~255)
// nif: 工作网口接口名称
// Owner: 是否为MASTER
// IPvX: IP协议类型(IPv4 或 IPv6)
func NewVirtualRouter(VRID byte, nif string, Owner bool, IPvX byte) (*VirtualRouter, error) {
	ift, err := net.InterfaceByName(nif)
	if err != nil {
		return nil, err
	}

	// 找到网口的IP地址
	preferred, err := interfacePreferIP(ift, IPvX)
	if err != nil {
		return nil, err
	}
	// RFC 5798 5.2.4. Priority
	var priority byte = 100
	if Owner {
		priority = 255
	}

	return NewVirtualRouterSpec(VRID, ift, preferred, priority)
}

// 设置 虚拟路由的优先级，如为主节点那么忽略
func (r *VirtualRouter) setPriority(Priority byte) *VirtualRouter {
	if r.priority == 255 {
		r.owner = true
	}

	r.priority = Priority
	return r
}

// SetAdvInterval 设置 VRRP消息发送间隔（心跳间隔），时间间隔不能小于 10 ms。
func (r *VirtualRouter) SetAdvInterval(Interval time.Duration) *VirtualRouter {
	if Interval < 10*time.Millisecond {
		// logg.Printf(INFO, "interval can less than 10 ms")
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
	// logg.Printf("set MasterAdvInterval skewTime: %d, masterDownInterval: %d\n", r.skewTime, r.masterDownInterval)
	// 从 MasterDownInterval 和 SkewTime 的计算方式来看，
	// 同一组VirtualRouter中，Priority 越高的Router越快地认为某个Master失效
	return r
}

// SetPreemptMode 设置 抢占模式
// 高优先级备份路由器是否抢占低优先级主路由器。
// 值为 true 表示 允许抢占，值为 false 表示 禁止抢占。默认值为 true。
func (r *VirtualRouter) SetPreemptMode(flag bool) *VirtualRouter {
	r.preempt = flag
	return r
}

// AddIPvXAddr 添加虚拟IP
func (r *VirtualRouter) AddIPvXAddr(ip net.IP) {
	if (r.ipvX == IPv4 && ip.To4() == nil) || (r.ipvX == IPv6 && ip.To16() == nil) {
		return
	}
	var bin []byte
	if r.ipvX == IPv4 {
		bin = ip.To4()
	} else {
		bin = ip.To16()
	}
	key, ok := netip.AddrFromSlice(bin)
	if !ok {
		return
	}
	logg.Printf("VRID [%d] VIP %v added", r.vrID, ip)
	r.protectedIPaddrs[key] = true
}

// RemoveIPvXAddr 移除 虚拟路由的虚拟IP地址
func (r *VirtualRouter) RemoveIPvXAddr(ip net.IP) {
	key, _ := netip.AddrFromSlice(ip)
	logg.Printf("VRID [%d] IP %v removed", r.vrID, ip)
	if _, ok := r.protectedIPaddrs[key]; ok {
		delete(r.protectedIPaddrs, key)
	}
}

// VRID 返回 虚拟路由的 ID
func (r *VirtualRouter) VRID() byte {
	return r.vrID
}

// 虚拟路由器的 Master 发送 VRRP Advertisement 消息 (心跳消息)
func (r *VirtualRouter) sendAdvertMessage() {
	//for k := range r.protectedIPaddrs {
	//	logg.Printf("VRID [%d] send advert message of IP %s", r.vrID, k.String())
	//}
	// 根据构造VRRP消息
	x := r.assembleVRRPPacket()
	// 发送 VRRP Advertisement 消息
	if err := r.vrrpConn.WriteMessage(x); err != nil {
		logg.Printf("ERROR sending vrrp message: %v", err)
	}
}

// assembleVRRPPacket 根据当前的虚拟路由信息组装 VRRP Advertisement 消息
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
	// 构造伪首部，用于计算校验码
	var pshdr PseudoHeader
	pshdr.Protocol = VRRPIPProtocolNumber
	if r.ipvX == IPv4 {
		pshdr.Daddr = VRRPMultiAddrIPv4
	} else {
		pshdr.Daddr = VRRPMultiAddrIPv6
	}
	pshdr.Len = uint16(packet.PacketSize())
	pshdr.Saddr = r.preferredSourceIP
	packet.SetCheckSum(&pshdr)
	return &packet
}

// fetchVRRPDaemon VRRP Advertisement 消息接收精灵，持续接收VRRP Advertisement 消息，收到的消息会被放入 packetQueue 队列中。
func (r *VirtualRouter) fetchVRRPDaemon() {
	logg.Printf("VRID [%d] fetch vrrp msg daemon start", r.vrID)
	for {
		if atomic.LoadUint32(&r.state) == INIT {
			// 如果虚拟路由器处于 INIT 状态，则停止接收 VRRP Advertisement 消息
			logg.Printf("VRID [%d] fetch vrrp msg daemon stopped", r.vrID)
			return
		}
		packet, err := r.vrrpConn.ReadMessage()
		if err != nil {
			logg.Printf("ERROR receive vrrp message: %v, fetch message will be stop", err)
			return
		}
		//logg.Printf("VRID [%d] received VRRP packet: \n%s\n\n", r.vrID, packet.String())
		if r.vrID != packet.GetVirtualRouterID() {
			// 忽略不同 VRID 的 VRRP Advertisement 消息
			continue
		}

		r.packetQueue <- packet
	}
}

// 初始化 心跳定时器
func (r *VirtualRouter) makeAdvertTicker() {
	r.advertisementTicker = time.NewTicker(time.Duration(r.advertisementInterval*10) * time.Millisecond)
}

// 停止心跳定时器
func (r *VirtualRouter) stopAdvertTicker() {
	r.advertisementTicker.Stop()
}

// makeMasterDownTimer 初始化 主节点下线倒计时器
func (r *VirtualRouter) makeMasterDownTimer() {
	if r.masterDownTimer == nil {
		r.masterDownTimer = time.NewTimer(time.Duration(r.masterDownInterval*10) * time.Millisecond)
	} else {
		r.resetMasterDownTimer()
	}
}

// 停止 主节点下线倒计时器
func (r *VirtualRouter) stopMasterDownTimer() {
	//logg.Printf("VRID [%d] master down timer stopped", r.vrID)
	if !r.masterDownTimer.Stop() {
		select {
		case <-r.masterDownTimer.C:
		default:
		}
		//logg.Printf( "VRID [%d] master down timer expired before we stop it, drain the channel", r.vrID)
	}
}

// resetMasterDownTimer 重置 主节点下线倒计时
func (r *VirtualRouter) resetMasterDownTimer() {
	r.stopMasterDownTimer()
	r.masterDownTimer.Reset(time.Duration(r.masterDownInterval*10) * time.Millisecond)
}

// 设置 主节点下线倒计时为 skewTime
func (r *VirtualRouter) resetMasterDownTimerToSkewTime() {
	r.stopMasterDownTimer()
	r.masterDownTimer.Reset(time.Duration(r.skewTime*10) * time.Millisecond)
}

// 当状态机状态发生变更时，调用对应的处理函数
func (r *VirtualRouter) stateChanged(t transition) {
	if work, ok := r.transitionHandler[t]; ok && work != nil {
		work(r)
		logg.Printf("VRID [%d] handler of transition [%s] called", r.vrID, t)
	}
	return
}

// GetPriority 获取 虚拟路由的优先级
func (r *VirtualRouter) GetPriority() byte {
	return r.priority
}

// GetState 获取 虚拟路由的状态
// return INIT | MASTER | BACKUP
func (r *VirtualRouter) GetState() uint32 {
	return atomic.LoadUint32(&r.state)
}

// GetInterface 获取 虚拟路由的工作网口
func (r *VirtualRouter) GetInterface() *net.Interface {
	return r.ift
}

// GetPreferredSourceIP 获取 虚拟路由的优先IP地址
func (r *VirtualRouter) GetPreferredSourceIP() net.IP {
	return r.preferredSourceIP
}

// GetAdvInterval 获取 虚拟路由的心跳发送间隔
func (r *VirtualRouter) GetAdvInterval() time.Duration {
	return time.Duration(r.advertisementInterval) * 10 * time.Millisecond
}

// GetPreempt 获取 虚拟路由的抢占模式
func (r *VirtualRouter) GetPreempt() bool {
	return r.preempt
}

// GetVIPs 获取 虚拟路由的保护IP地址
func (r *VirtualRouter) GetVIPs() []net.IP {
	vips := make([]net.IP, 0)
	for k := range r.protectedIPaddrs {
		vips = append(vips, k.AsSlice())
	}
	return vips
}

// largerThan 比较IP数值大小 ip1 > ip2 （用于在优先级相同时IP大的优先）
func largerThan(ip1, ip2 net.IP) bool {
	if len(ip1) != len(ip2) {
		//logg.Printf(FATAL, "largerThan: two compared IP addresses must have the same length")
		return false
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

// stateMachine 状态机
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
func (r *VirtualRouter) stateMachine() {
	defer r.close()
	for {
		switch r.state {
		case INIT:

			select {
			case event := <-r.eventChannel:
				if event == START {
					logg.Printf("VRID [%d] event %v received", r.vrID, event)

					// 监听VRRP消息
					go r.fetchVRRPDaemon()

					if r.priority == 255 || r.owner {
						logg.Printf("VRID [%d] enter owner mode", r.vrID)
						r.sendAdvertMessage()
						if err := r.addrAnnouncer.AnnounceAll(r); err != nil {
							logg.Printf("ERROR INIT to MASTER gratuitous arp sending: %v", err)
						}
						// 设置广播定时器
						r.makeAdvertTicker()

						logg.Printf("VRID [%d] enter MASTER state", r.vrID)
						atomic.StoreUint32(&r.state, MASTER)
						r.stateChanged(Init2Master)
					} else {
						logg.Printf("VRID [%d] VR is not the owner of protected IP addresses", r.vrID)
						r.setMasterAdvInterval(r.advertisementIntervalOfMaster)
						// set up master down timer
						r.makeMasterDownTimer()
						logg.Printf("VRID [%d] enter BACKUP state", r.vrID)
						atomic.StoreUint32(&r.state, BACKUP)
						r.stateChanged(Init2Backup)
					}
				} else if event == SHUTDOWN {
					logg.Printf("VRID [%d] SHUTDOWN close state machine.", r.vrID)
					return
				}
			}

		case MASTER:

			select {
			case event := <-r.eventChannel:
				// 收到 shutdown 事件
				if event == SHUTDOWN {
					logg.Printf("VRID [%d] SHUTDOWN event received virtual route will be close.", r.vrID)
					// 关闭心跳包定时器
					r.stopAdvertTicker()
					// 设置优先级为 0（表示让渡主节点），并广播发送消息
					var priority = r.priority
					r.setPriority(0)
					r.sendAdvertMessage()

					r.setPriority(priority)
					// 进入初始化状态
					atomic.StoreUint32(&r.state, INIT)
					r.stateChanged(Master2Init)
					logg.Printf("VRID [%d] SHUTDOWN event received virtual route will reset to init state.", r.vrID)
				}
			case <-r.advertisementTicker.C:
				// 心跳包定时器到期，发送心跳包
				r.sendAdvertMessage()
			case packet := <-r.packetQueue:
				// 优先级比主节点高，或者 优先级相同但是源IP比主节点的优先源IP大
				// 那么认为 收到了一个更高优先级的主节点的心跳包，主节点让渡
				if packet.GetPriority() > r.priority ||
					(packet.GetPriority() == r.priority && largerThan(packet.Pshdr.Saddr, r.preferredSourceIP)) {
					// 停止心跳包定时器
					r.stopAdvertTicker()
					// 设置新的主节点心跳消息发送定时器
					r.setMasterAdvInterval(packet.GetAdvertisementInterval())
					// 初始化主节点下线倒计时
					r.makeMasterDownTimer()
					// 切换状态至备份节点
					atomic.StoreUint32(&r.state, BACKUP)
					r.stateChanged(Master2Backup)
				} else {
					// 忽略优先级低的所有消息
				}
			}

		case BACKUP:

			select {
			case event := <-r.eventChannel:

				if event == SHUTDOWN {
					// 关闭主节点下线倒计时
					r.stopMasterDownTimer()
					// 设置状态为 初始化
					atomic.StoreUint32(&r.state, INIT)
					r.stateChanged(Backup2Init)
					logg.Printf("VRID [%d] SHUTDOWN event received virtual route will reset to init state.", r.vrID)
					//return
				}

			case packet := <-r.packetQueue:
				//logg.Printf("VRID [%d] received a packet from %s priority %d", r.vrID, packet.Pshdr.Saddr.String(), packet.GetPriority())
				// 收到心跳包
				if packet.GetPriority() == 0 {
					// 若心跳包优先级为 0，那么认为主节点让渡，设置主节点下线倒计时为 Skew_Time，进入选举状态
					logg.Printf("VRID [%d] received an advertisement with priority 0, transit into MASTER state", r.vrID)
					// 设置 Master_Down_Timer 为 Skew_Time 进入选举状态
					r.resetMasterDownTimerToSkewTime()
				} else {
					// 若为非抢占模式，无论收到的心跳包优先级如何，都认为是来自主节点的心跳包
					// 继续保持 BACKUP 状态
					//
					// 若收到的心跳包优先级比备份节点优先级高；
					// 若优先级相同但是源IP比备份节点的优先源IP大；
					// 那么 认为是来自主节点的心跳包。
					// 继续保持 BACKUP 状态
					if r.preempt == false ||
						packet.GetPriority() > r.priority ||
						(packet.GetPriority() == r.priority && largerThan(packet.Pshdr.Saddr, r.preferredSourceIP)) {
						// 重置主节点下线倒计时器
						r.setMasterAdvInterval(packet.GetAdvertisementInterval())
						r.resetMasterDownTimer()
					}
				}

			case <-r.masterDownTimer.C:
				logg.Printf("VRID [%d] enter MASTER state", r.vrID)
				// 主节点下线倒计时到期，进入选举状态
				// 组播当前节点的心跳消息，表示当前节点想要成为主节点
				r.sendAdvertMessage()
				// 发送ARP消息告知广播域内的主机当前主机接管了虚拟路由器的IP地址
				if err := r.addrAnnouncer.AnnounceAll(r); err != nil {
					logg.Printf("ERROR BACKUP to MASTER sending gratuitous arp: %v", err)
				}
				// Set the Advertisement Timer to Advertisement interval
				r.makeAdvertTicker()
				// 进入主节点状态
				atomic.StoreUint32(&r.state, MASTER)
				r.stateChanged(Backup2Master)
			}
		}

	}
}

// AddEventListener 添加状态机事件监听器
// typ: 状态变更类型
// handler: 状态变更时的回调函数
//
// return: 如果已经存在该类型的监听器，那么返回 true，否则返回 false
func (r *VirtualRouter) AddEventListener(typ transition, handler func(*VirtualRouter)) bool {
	_, exist := r.transitionHandler[typ]
	if exist {
		r.transitionHandler[typ] = handler
		return true
	} else {
		r.transitionHandler[typ] = handler
	}
	return exist
}

// Start 启动虚拟路由器
// 虚拟路由器启动后，将开始监听VRRP消息，根据状态机的状态，切换至不同的状态。
func (r *VirtualRouter) Start() {
	// 发送启动命令
	go func() {
		r.eventChannel <- START
	}()
	// 启动状态机
	r.stateMachine()
}

// Stop 停止虚拟路由器
func (r *VirtualRouter) Stop() {
	// 若不为 INIT 状态，
	// 向发送停止命令，使状态机进入 INIT 状态。
	if atomic.LoadUint32(&r.state) != INIT {
		r.eventChannel <- SHUTDOWN
	}
	// 终止并退出状态机
	go func() {
		r.eventChannel <- SHUTDOWN
	}()
}

// 关闭连接回收资源
func (r *VirtualRouter) close() {
	if r == nil {
		return
	}
	if r.addrAnnouncer != nil {
		_ = r.addrAnnouncer.Close()
	}
	if r.vrrpConn != nil {
		_ = r.vrrpConn.Close()
	}
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
