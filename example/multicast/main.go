package main

import (
	"encoding/hex"
	"golang.org/x/net/ipv4"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// 网口名称
	name := "以太网"
	// 本地地址
	localAddr := &net.IPAddr{IP: net.IPv4(0, 0, 0, 0)}
	// 组播地址
	multiAddr := &net.IPAddr{IP: net.IPv4(224, 0, 0, 18)}

	itf, _ := net.InterfaceByName(name)
	conn, err := net.ListenIP("ip4:112", localAddr)
	if err != nil {
		log.Printf("网口 [%s] 无法监听组播消息， %v\n", itf.Name, err)
		return
	}
	defer conn.Close()

	pc := ipv4.NewPacketConn(conn)
	_ = pc.LeaveGroup(itf, multiAddr)
	if err = pc.JoinGroup(itf, multiAddr); err != nil {
		log.Printf("网口 [%s] 无法加入组播， %v\n", itf.Name, err)
		return
	}
	defer pc.LeaveGroup(itf, multiAddr)

	if err = pc.SetMulticastLoopback(true); err != nil {
		log.Printf("网口 [%s] 无法设置组播回环， %v\n", itf.Name, err)
	}
	_ = pc.SetMulticastTTL(255)
	_ = pc.SetMulticastInterface(itf)
	_ = pc.SetControlMessage(ipv4.FlagTTL|ipv4.FlagSrc|ipv4.FlagDst|ipv4.FlagInterface, true)

	_ = conn.SetReadBuffer(2048)
	_ = conn.SetWriteBuffer(2048)

	// 监听组播消息
	go func() {
		for {
			buffer := make([]byte, 2048)
			n, _, src, err := pc.ReadFrom(buffer)
			if err != nil {
				log.Printf("网口 [%s] 停止监听\n", itf.Name)
				return
			}
			log.Printf("%s:\n%s\n", src.String(), hex.Dump(buffer[:n]))
		}
	}()

	// 发送组播消息
	go func() {
		raw, _ := hex.DecodeString("31f0640100640608c0a800e6")
		_ = &ipv4.ControlMessage{TTL: 255, IfIndex: itf.Index}
		for {
			time.Sleep(time.Millisecond * 800)
			_, err := pc.WriteTo(raw, nil, multiAddr)
			if err != nil {
				log.Printf("网口 [%s] 发送组播消息失败， %v\n", itf.Name, err)
				return
			}
			log.Printf("网口 [%s] 发送组播消息成功\n", itf.Name)
		}
	}()

	sigout := make(chan os.Signal, 1)
	signal.Notify(sigout, os.Kill, os.Interrupt, syscall.SIGTERM)
	<-sigout
	log.Printf("收到信号")
	_ = pc.Close()
	log.Printf("网口 [%s] 停止监听\n", itf.Name)
}
