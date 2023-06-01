# Go VRRP

[![Documentation](https://godoc.org/github.com/Trisia/govrrp?status.svg)](https://pkg.go.dev/github.com/Trisia/govrrp) ![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/Trisia/govrrp) ![GitHub tag (latest SemVer)](https://img.shields.io/github/v/tag/Trisia/govrrp) 

> - 致谢 [napw](https://github.com/napw) forked from [github.com/napw/VRRP-go](https://github.com/napw/VRRP-go)。

Go实现的 **V**irtual **R**outer **R**edundancy **P**rotocol (VRRP) 协议（V3），协议详见 [RFC 5798](https://tools.ietf.org/html/rfc5798)。

VRRP协议用于路由器的冗余，协议通过组播的方式定期发送“心跳” 通知同组节点，组内各节点在心跳丢失后根据协议实现节点的选举。

注意：虚拟IP通常使用 ARP协议中的Gratuitous ARP来实现，由主节点定期发出。

## 快速开始

```go
package demo

import (
	"flag"
	"fmt"
	"github.com/Trisia/govrrp"
	"time"
)

var (
	VRID     int
	Priority int
)

func init() {
	flag.IntVar(&VRID, "vrid", 233, "virtual router ID")
	flag.IntVar(&Priority, "pri", 100, "router priority")
}

func main() {
	flag.Parse()
	var vr = govrrp.NewVirtualRouter(byte(VRID), "ens33", false, govrrp.IPv4)
	vr.SetPriorityAndMasterAdvInterval(byte(Priority), time.Millisecond*800)
	vr.Enroll(govrrp.Backup2Master, func() {
		fmt.Println("init to master")
	})
	vr.Enroll(govrrp.Master2Init, func() {
		fmt.Println("master to init")
	})
	vr.Enroll(govrrp.Master2Backup, func() {
		fmt.Println("master to backup")
	})
	go func() {
		time.Sleep(time.Minute * 5)
		vr.Stop()
	}()
	vr.StartWithEventSelector()
}

```

编译

```shell
cd demo

GOOS=linux go build -o vr
```

在MASTER节点上执行：

```bash
#execute on MASTER NODE
./vr -vrid=200 -pri=150
```

在BACKUP节点上执行：

```bash
#execute on BACKUP NODE
./vr -vrid=200 -pri=230
```

