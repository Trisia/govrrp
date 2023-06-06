# Go VRRP

[![Github CI](https://github.com/Trisia/govrrp/actions/workflows/ci.yml/badge.svg)](https://github.com/Trisia/govrrp/actions/workflows/ci.yml)
[![Documentation](https://godoc.org/github.com/Trisia/govrrp?status.svg)](https://godoc.org/github.com/Trisia/govrrp)
[![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/Trisia/govrrp)](https://github.com/Trisia/govrrp/blob/master/go.mod)
[![GitHub tag (latest SemVer)](https://img.shields.io/github/v/tag/Trisia/govrrp)](https://github.com/Trisia/govrrp/tags)

> - 致谢 [napw](https://github.com/napw) forked from [github.com/napw/VRRP-go](https://github.com/napw/VRRP-go)。

Go实现的 **V**irtual **R**outer **R**edundancy **P**rotocol (VRRP) 协议（V3），协议详见 [RFC 5798](https://tools.ietf.org/html/rfc5798)。

VRRP协议用于路由器的冗余，协议通过组播的方式定期发送“心跳” 通知同组节点，组内各节点在心跳丢失后根据协议实现节点的选举。

虚拟IP使用 ARP协议中的Gratuitous ARP来实现，由主节点定期以广播形式发出。

**仅支持 Linux！**

![img.png](demo/img.png)

## 快速开始

安装依赖

```bash
go get -u github.com/Trisia/govrrp
```

相关文档：

- [GoVRRP API文档](https://pkg.go.dev/github.com/Trisia/govrrp)
- [GoVRRP Demo示例](demo/README.md)


下面创建一个简单的VRRP实例，实现一个虚拟路由器。

```go
package main

import (
	"github.com/Trisia/govrrp"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)


func main() {
	// 创建虚拟路由器，设置VRID为240，使用网卡eno1，采用IPv4协议
	vr, err := govrrp.NewVirtualRouter(240, "eno1", false, govrrp.IPv4)
	if err != nil {
		log.Fatal(err)
	}
    // 设置路由 优先级 和 心跳时间
	vr.SetPriorityAndMasterAdvInterval(100, time.Millisecond*800)
	// 设置虚拟IP
	vr.AddIPvXAddr(net.ParseIP("192.168.0.230"))

	// 注册事件监听
	vr.AddEventListener(govrrp.Backup2Master, func() {
		log.Printf("VRID [%d] init to master\n", vr.VRID())
	})
	vr.AddEventListener(govrrp.Master2Init, func() {
		log.Printf("VRID [%d] master to init\n", vr.VRID())
	})
	vr.AddEventListener(govrrp.Master2Backup, func() {
		log.Printf("VRID [%d] master to backup\n", vr.VRID())
	})
	
	go vr.Start()

	sigout := make(chan os.Signal, 1)
	signal.Notify(sigout, os.Kill, os.Interrupt, syscall.SIGTERM)
	<-sigout
	
	vr.Stop()

	log.Println("wait for virtual router to stop...")
	time.Sleep(time.Second)
}
```

