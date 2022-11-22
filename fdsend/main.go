package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const serverAddr = "127.0.0.1:7000"

var cache = make(map[string]uintptr)

func main() {
	lis, err := net.Listen("tcp", serverAddr)
	if err != nil {
		panic(err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGKILL, syscall.SIGINT)
	stop := make(chan struct{})
	exit := make(chan struct{})

	go dataSocketListener(lis, stop, exit)

	<-sig

	lis.Close()
	stop <- struct{}{}

	err = sendSocket(lis.(*net.TCPListener))
	if err != nil {
		fmt.Println("failed to send listener fd:", err)
	}

	exit <- struct{}{}

	time.Sleep(1 * time.Second)

	log.Println("Bye bye")
}

func sendListener(lis *net.TCPListener) error {
	// connect to the unix socket
	const udsPath = "/tmp/uds.sock"
	conn, err := net.Dial("unix", udsPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	connFd, err := getConnFd(conn.(*net.UnixConn))
	if err != nil {
		return err
	}

	// pass listener fd
	lisFd, err := getConnFd(lis)
	if err != nil {
		return err
	}
	rights := syscall.UnixRights(int(lisFd))
	return syscall.Sendmsg(connFd, nil, rights, nil, 0)
}

func sendSocket(lis *net.TCPListener) error {
	// connect to the unix socket
	const udsPath = "/tmp/transfer.sock"
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
					//fmt.Println("error from read:", err)
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
