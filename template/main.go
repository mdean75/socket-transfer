package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/coreos/go-systemd/v22/dbus"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
)

const (
	serverAddr = "127.0.0.1:7000"
	udsPath    = "/tmp/transfer.sock"
)

var cache = make(map[string]uintptr)

func main() {

	conn, err := dbus.NewSystemConnectionContext(context.TODO())
	if err != nil {
		fmt.Println(err)
	}

	upgrade, err := isUpgrade(conn)
	if err != nil {
		fmt.Println(err)
		return
	}

	if upgrade {
		err := handleUpgrade(conn)
		if err != nil {
			fmt.Println(err)
			return
		}
	}

	lis, err := net.Listen("tcp", serverAddr)
	if err != nil {
		panic(err)
	}

	stop := make(chan struct{})
	exit := make(chan struct{})

	go dataSocketListener(lis, stop, exit)

	sig := make(chan os.Signal, 1)
	kill := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGUSR1, syscall.SIGUSR2)
	signal.Notify(kill, syscall.SIGTERM)

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()

	Loop:
		for {
			s := <-sig
			fmt.Println(os.Getpid(), "reveived signal:", s)

			switch s {
			case syscall.SIGUSR1:
				name, err := whoami(conn)
				if err != nil {
					fmt.Println(err)
					continue
				}

				name = strings.TrimLeft(name, "tems@")
				name = strings.TrimRight(name, ".service")

				var newVersion string
				if name == "a" {
					newVersion = "b"
				} else {
					newVersion = "a"
				}

				ch := make(chan string)
				_, err = conn.StartUnitContext(context.TODO(), fmt.Sprintf("tems@%s.service", newVersion), "fail", ch)
				if err != nil {
					fmt.Println(err) // ??? better handling
				}
				status := <-ch
				fmt.Println(status)

			case syscall.SIGUSR2:
				err := sendSocket()
				if err != nil {
					return
				}
				break Loop
			}
		}
	}()
	wg.Wait()

	fmt.Println("wait for SIGTERM")
	<-kill
}

func sendSocket() error {

	fmt.Println("send sockets")
	defer func() {
		fmt.Println("done sending sockets")
	}()
	// connect to the unit socket

	conn, err := net.Dial("unix", udsPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	connFd, err := getConnFd(conn.(*net.UnixConn))
	if err != nil {
		return err
	}

	var fds []uintptr
	for _, fd := range cache {
		fds = append(fds, fd)
	}

	rights := syscall.UnixRights(int(fds[0]))
	return syscall.Sendmsg(connFd, nil, rights, nil, 0)
}

func getConnFd(conn *net.UnixConn) (connFd int, err error) {
	var rawConn syscall.RawConn
	rawConn, err = conn.SyscallConn()
	if err != nil {
		return
	}

	err = rawConn.Control(func(fd uintptr) {
		connFd = int(fd)
	})
	return
}

func dataSocketListener(lis net.Listener, stop, exit chan struct{}) {
	fmt.Println("listen to data socket:", lis.Addr())

	defer lis.Close()
	for {
		conn, err := lis.Accept()
		if err != nil {
			fmt.Println("data transfer listener accept error:", err)
			return
		}

		go func(c *net.TCPConn) {

			f, err := c.File()
			if err != nil {
				fmt.Println("error getting c.file", err)
				return
			}
			defer func() {
				c.Close()
				f.Close()
				fmt.Printf("removing %s from cache\n", f.Name())
				delete(cache, f.Name())
			}()

			fmt.Printf("adding %s to cache\n", f.Name())
			cache[f.Name()] = f.Fd()

			buf := make([]byte, 1<<10)
		readLoop:
			for {
				select {
				case <-stop:
					fmt.Println("receive on stop chan")
					break readLoop
				default:

				}
				n, err := c.Read(buf)
				if err != nil {
					if !errors.Is(err, io.EOF) {
						fmt.Println("transfer socket listener error:", err)
					}
					return
				}

				fmt.Printf("read %d bytes from %s to %s data=%s\n", n, conn.RemoteAddr(), conn.LocalAddr(), string(buf))
			}

			fmt.Println("wait for exit")
			<-exit
		}(conn.(*net.TCPConn))
	}
}

func handleUpgrade(conn *dbus.Conn) error {
	name, err := whoami(conn)
	if err != nil {
		fmt.Println(err)
		return err
	}

	units, err := getTemplateUnits(conn)
	if err != nil {
		fmt.Println(err)
		return err
	}

	var oldService string
	for _, unit := range units {
		if unit != name {
			oldService = unit
		}
	}

	receiveEstablishedConns(oldService)

	// last step of the upgrade is to shut down the old instance
	for _, unit := range units {
		if unit == name {
			continue
		}
		ch := make(chan string)
		fmt.Println("stopping unit:", unit)
		_, err := conn.StopUnitContext(context.TODO(), unit, "fail", ch)
		if err != nil {
			fmt.Println(err)
			return err
		}
		out := <-ch
		fmt.Println("result of stop unit:", out)
	}
	return nil
}

func receiveEstablishedConns(oldService string) {
	transferListener, err := net.Listen("unix", udsPath)
	if err != nil {
		panic(err)
	}
	defer transferListener.Close()

	var conn net.Conn
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()

		conn, err = transferListener.Accept()
		if err != nil {
			panic(err)
		}
		transferListener.Close()
		return
	}()

	// need to get the old service name
	fmt.Println("send USR2 to:", oldService)
	cmd := exec.Command("systemctl", "kill", "--signal=USR2", oldService)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Start()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Start: "+err.Error())
		return
	}

	wg.Wait()
	fmt.Println("Connection accepted for socket transfer")

	establishedConn := receiveConn(conn.(*net.UnixConn))
	fmt.Println("received socket:", establishedConn.LocalAddr(), establishedConn.RemoteAddr())
	tcpcon := establishedConn.(*net.TCPConn)
	f, err := tcpcon.File()
	if err != nil {
		fmt.Println("error getting c.file", err)
		return
	}
	cache[f.Name()] = f.Fd()
	go func() {
		defer func() {
			tcpcon.Close()
			f.Close()
			fmt.Printf("removing %s from cache\n", f.Name())
			delete(cache, f.Name())
		}()
		for {
			buf := make([]byte, 1<<10)
			n, err := establishedConn.Read(buf)
			if err != nil {
				fmt.Println(err)
				break
			}
			fmt.Printf("received %d bytes, data: %s", n, string(buf))
		}
	}()
}

func receiveConn(conn *net.UnixConn) net.Conn {

	cf, err := conn.File()
	if err != nil {
		panic(err)
	}
	ffd := int(cf.Fd())

	// receive socket control message
	b := make([]byte, syscall.CmsgSpace(4))
	_, _, _, _, err = syscall.Recvmsg(ffd, nil, b, 0)
	if err != nil {
		panic(err)
	}

	// parse socket control message
	cmsgs, err := syscall.ParseSocketControlMessage(b)
	if err != nil {
		panic(err)
	}
	fds, err := syscall.ParseUnixRights(&cmsgs[0])
	if err != nil {
		panic(err)
	}
	fd := fds[0]
	fmt.Printf("Got socket fd %d\n", fd)

	// construct net conn
	f := os.NewFile(uintptr(fd), "conn")
	defer f.Close()

	l, err := net.FileConn(f)
	if err != nil {
		panic(err)
	}

	return l
}

func whoami(conn *dbus.Conn) (string, error) {
	return conn.GetUnitNameByPID(context.TODO(), uint32(os.Getpid()))
}

func isUpgrade(conn *dbus.Conn) (bool, error) {
	units, err := getTemplateUnits(conn)
	if err != nil {
		fmt.Println(err)
		return false, err
	}

	if len(units) == 1 {
		return false, nil
	} else if len(units) == 2 {
		return true, nil
	} else {
		return false, errors.New("invalid number of instances running")
	}
}

func getTemplateUnits(conn *dbus.Conn) ([]string, error) {
	// this fetches all loaded units so will need to filter the list manually, unable to get the list by patterns working
	unitStatuses, err := conn.ListUnitsContext(context.TODO())
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	units := make([]string, 0)
	for _, unit := range unitStatuses {
		if !strings.Contains(unit.Name, "tems@") {
			continue
		}
		units = append(units, unit.Name)
	}

	return units, nil
}
