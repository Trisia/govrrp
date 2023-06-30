package main

import (
	"flag"
	"github.com/Trisia/govrrp"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var (
	VRID     int    // 虚拟路由ID
	Priority int    // 优先级
	Nif      string // 网口名称
	Typ      int    // 协议类型 4:IPv4 6:IPv6
	VIP      string // 虚拟IP地址
	Mill     int    // 发送间隔毫秒数
	preempt  bool   // 抢占模式
)

func init() {
	flag.IntVar(&VRID, "id", 240, "虚拟路由ID (1~255)")
	flag.IntVar(&Priority, "p", 100, "虚拟路由器优先级(1~255)，255表示主机拥有者")
	flag.StringVar(&Nif, "i", "eno1", "网卡名称")
	flag.IntVar(&Typ, "t", 4, "虚拟路由器类型(4:IPv4 6:IPv6)")
	flag.StringVar(&VIP, "vip", "", "虚拟IP地址")
	flag.IntVar(&Mill, "itl", 800, "发送间隔毫秒数")
	flag.BoolVar(&preempt, "pp", false, "抢占模式")
}

func main() {

	flag.Parse()
	if Nif == "" {
		log.Fatal("-i 网卡名称不能为空")
	}
	if byte(Typ) != govrrp.IPv4 && byte(Typ) != govrrp.IPv6 {
		log.Fatal("-t 虚拟路由器类型错误")
	}
	addr := net.ParseIP(VIP)
	if VIP == "" || addr == nil {
		log.Fatal("-vip 虚拟IP地址错误")
	}
	log.Printf("VRID: %d, Priority: %d, NIC: %s, VIP: %s, Mill: %d, preempt: %v\n", VRID, Priority, Nif, VIP, Mill, preempt)
	vr, err := govrrp.NewVirtualRouter(byte(VRID), Nif, Priority == 255, byte(Typ))
	if err != nil {
		log.Fatal(err)
	}
	vr.SetPreemptMode(preempt)
	vr.SetAdvInterval(time.Millisecond * time.Duration(Mill))
	vr.SetPriorityAndMasterAdvInterval(byte(Priority), time.Millisecond*time.Duration(Mill))
	vr.AddIPvXAddr(addr)

	vr.AddEventListener(govrrp.Init2Master, func(ctx *govrrp.VirtualRouter) {
		log.Printf("VRID [%d] init to master\n", ctx.VRID())

	})
	vr.AddEventListener(govrrp.Backup2Master, func(ctx *govrrp.VirtualRouter) {
		log.Printf("VRID [%d] backup to master\n", vr.VRID())

	})
	vr.AddEventListener(govrrp.Master2Init, func(ctx *govrrp.VirtualRouter) {
		log.Printf("VRID [%d] master to init\n", ctx.VRID())

	})
	vr.AddEventListener(govrrp.Master2Backup, func(ctx *govrrp.VirtualRouter) {
		log.Printf("VRID [%d] master to backup\n", ctx.VRID())
	})
	go vr.Start()

	sigout := make(chan os.Signal, 1)
	signal.Notify(sigout, os.Kill, os.Interrupt, syscall.SIGTERM)
	<-sigout
	vr.Stop()

	log.Println("wait for virtual router to stop...")
	time.Sleep(time.Second)
}
