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
