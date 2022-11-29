package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

var version = "0.3.0"

const serverAddr = "127.0.0.1:7000"

var cache = make(map[string]uintptr)

// main.go
func main() {
	defer func() {
		fmt.Println("SHUTTING DOWN:", os.Getpid())
	}()

	lis, err := net.Listen("tcp", serverAddr)
	if err != nil {
		panic(err)
	}

	stop := make(chan struct{})
	exit := make(chan struct{})

	go dataSocketListener(lis, stop, exit)

	// will exit 0 if not running, non-zero if the upgrade service is running
	cmd := exec.Command("systemctl", "is-active", "upgrade.service")

	_, err = cmd.Output()
	if err != nil {
		fmt.Println(err)
	} else {
		fmt.Println("upgrade path")
		handleUpgrade()
	}

	fmt.Printf("STARTUP SERVICE: (PID: %d, Version: %s)\n", os.Getpid(), version)
	sig := make(chan os.Signal, 1)
	kill := make(chan os.Signal, 1)

	signal.Notify(sig, syscall.SIGUSR2, syscall.SIGUSR1)
	signal.Notify(kill, syscall.SIGTERM, syscall.SIGKILL)

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
	Loop:
		for {
			s := <-sig
			fmt.Println(os.Getpid(), "received signal:", s)

			switch s {
			case syscall.SIGUSR1:
				cmd := exec.Command("systemctl", "start", "upgrade.service")
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr

				err := cmd.Start()
				if err != nil {
					_, _ = fmt.Fprintf(os.Stderr, "Start: "+err.Error())
					return
				}

				// Wait, then send signal
				time.Sleep(time.Second * 2)
				fmt.Println("Started upgrade service")

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

	return
}

func handleUpgrade() {
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

	fmt.Println("send USR1 to upgrade service")
	cmd := exec.Command("systemctl", "kill", "--signal=USR1", "upgrade.service")
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

			buf := make([]byte, 1<<10) // 1024
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

func getConnFd(conn syscall.Conn) (connFd int, err error) {
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

const udsPath = "/tmp/transfer.sock"

func sendSocket() error {

	fmt.Println("send sockets")
	defer func() {
		fmt.Println("done sending sockets")
	}()
	// connect to the unix socket

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
