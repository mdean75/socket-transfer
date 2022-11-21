package main

import (
	"errors"
	"fmt"
	"github.com/coreos/go-systemd/activation"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

/*
  1. On startup read the fds from the fd transfer socket
  2. Close the fd transfer listener
  3. Connect to all prior sockets
  4. On shutdown send all fds to the fd transfer socket
*/

var cache = make(map[string]uintptr)

func main() {
	fmt.Println("service started with PID:", os.Getpid())
	listeners, err := activation.Listeners()
	if err != nil {
		fmt.Println(err)
		return
	}

	for _, lis := range listeners {
		//fmt.Println(lis.Addr())

		if lis.Addr().String() == "/tmp/uds.sock" {
			go transferSocketListener(lis)
		} else {
			go dataSocketListener(lis)

			//go func(listener net.Listener) {
			//	defer listener.Close()
			//
			//	for {
			//		conn, err := listener.Accept()
			//		if err != nil {
			//			fmt.Println("error listener accept", err)
			//			return
			//		}
			//
			//		go func(c net.Conn) {
			//			defer func() {
			//				c.Close()
			//			}()
			//			for {
			//				b := make([]byte, 100)
			//				n, err := c.Read(b)
			//				if err != nil {
			//					if errors.Is(err, io.EOF) {
			//						fmt.Println("client closed connection")
			//						break
			//					}
			//					fmt.Println("c.Read(b)", err)
			//					break
			//				}
			//
			//				fmt.Printf("read %d bytes: %s", n, string(b))
			//
			//				fmt.Println("responding with receipt ack")
			//				//n, err = c.Write([]byte("ACK\n"))
			//				n, err = c.Write([]byte(fmt.Sprintf("%x\n", "C")))
			//				if err != nil {
			//					fmt.Println("write failed", err)
			//				}
			//			}
			//
			//		}(conn)
			//	}
			//
			//}(lis)
		}

	}

	//kill := make(chan struct{}, 1)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT, syscall.SIGKILL)

	fmt.Println("wait for os signal")
	<-stop
	fmt.Println("os signal received, shut down")

	conn, err := net.Dial("unix", "/tmp/uds.sock")
	if err != nil {
		fmt.Println("error dialing uds sock", err)
		return
	}

	var fds []int
	fmt.Printf("socket cache: %+v\n", cache)
	for k, v := range cache {
		fmt.Println("cache:", k, v)
		fds = append(fds, int(cache[k]))
	}

	//for _, fd := range cache {
	//	//rights := syscall.UnixRights(int(fd))
	//	//n, oobn, err := syscall.Sendmsg([]byte{0}, rights, nil)
	//	//conn.Write([]byte(strconv.Itoa(int(fd))))
	//	//conn.Write([]byte(name))
	//	size := unsafe.Sizeof(fd)
	//	b := make([]byte, size)
	//
	//	switch size {
	//	case 4:
	//		binary.LittleEndian.PutUint32(b, uint32(fd))
	//	case 8:
	//		binary.LittleEndian.PutUint64(b, uint64(fd))
	//	default:
	//		panic(fmt.Sprintf("unknown uintptr size: %v", size))
	//	}
	//
	//	conn.Write(b)
	//}

	fmt.Println("send fds to unix socket using syscall and sendmsg")
	rights := syscall.UnixRights(fds...)

	unixConn := conn.(*net.UnixConn)
	fd, err := unixConn.File()
	if err != nil {
		fmt.Println("error getting unixConn file", err)
		return
	}
	socketFD := int(fd.Fd())

	defer fd.Close()
	defer unixConn.Close()

	err = syscall.Sendmsg(socketFD, []byte{0}, rights, nil, 0)
	if err != nil {
		fmt.Println("error syscall.Sendmsg", err)
	}

	fmt.Println("successfully sent fds, close connection")
	conn.Close()

	//fmt.Println("wrote all fds to uds sock, closing connection")

}

func transferSocketListener(lis net.Listener) {
	fmt.Println("listen to fd transfer socket")

	ulis := lis.(*net.UnixListener)
	ulis.SetDeadline(time.Now().Add(time.Second * 10))
	conn, err := ulis.Accept()
	if err != nil {
		fmt.Println("socket transfer listener accept error:", err)
		fmt.Println("close listener")
		lis.Close()
		return
	}
	//defer conn.Close()
	defer func() {
		fmt.Println("exiting transfer socket listener")
		conn.Close()
		lis.Close()
	}()

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			buf := make([]byte, 1<<10) // 1024 bytes

			n, err := conn.Read(buf)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					fmt.Println("transfer socket listener error:", err)
				}
				return
			}

			fmt.Printf("read %d bytes from %s to %s data=%s\n", n, conn.RemoteAddr(), conn.LocalAddr(), string(buf))

			if err != nil {
				panic(err)
			}

			unixConn := conn.(*net.UnixConn)
			fd, err := unixConn.File()
			if err != nil {
				fmt.Println("error getting unixConn file", err)
				return
			}
			socketFD := int(fd.Fd())

			buf = make([]byte, syscall.CmsgSpace(4))
			_, _, _, _, err = syscall.Recvmsg(socketFD, nil, buf, 0)
			if err != nil {
				return
			}
			if err != nil {
				panic(err)
			}

			var msgs []syscall.SocketControlMessage
			msgs, err = syscall.ParseSocketControlMessage(buf)
			var allfds []int
			for i := 0; i < len(msgs) && err == nil; i++ {
				var msgfds []int
				msgfds, err = syscall.ParseUnixRights(&msgs[i])
				allfds = append(allfds, msgfds...)
			}
			fmt.Println("allfds:", allfds)
			//fp := **(**uintptr)(unsafe.Pointer(&buf))
			//f := os.NewFile(fp, "test-new-sock")
			//if f == nil {
			//	fmt.Println("new sock is nil")
			//	continue
			//}
			//newLis, err := net.FileListener(f)
			//if err != nil {
			//	fmt.Println("file listener error", err)
			//	continue
			//}
			//go dataSocketListener(newLis)
		}
	}()
	wg.Wait()
}

func dataSocketListener(lis net.Listener) {
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
			for {
				n, err := c.Read(buf)
				if err != nil {
					if !errors.Is(err, io.EOF) {
						fmt.Println("transfer socket listener error:", err)
					}
					return
				}

				fmt.Printf("read %d bytes from %s to %s data=%s\n", n, conn.RemoteAddr(), conn.LocalAddr(), string(buf))
			}
		}(conn.(*net.TCPConn))
	}
}
