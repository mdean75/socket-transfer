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
)

const udsPath = "/tmp/transfer.sock"

func main() {
	os.Remove(udsPath) //nolint: errcheck
	lis, err := net.Listen("unix", udsPath)
	if err != nil {
		panic(err)
	}
	defer lis.Close()

	log.Println("Wait receiving listener ...")
	conn, err := lis.Accept()
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	c := receiveConn(conn.(*net.UnixConn)) // this is a net.Listener
	//http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
	//	fmt.Fprintf(w, "[server2] Hello, world!")
	//})
	//log.Printf("Server is listening on %s ...\n", httpLis.Addr())
	//http.Serve(httpLis, nil)
	//h := httpLis.(*net.TCPListener)
	//ff, _ := h.File()
	//fmt.Println(ff.Name())
	//go dataSocketListener(httpLis)
	//conn, err = httpLis.Accept()
	//if err != nil {
	//	fmt.Println(err)
	//	return
	//}

	for {
		buf := make([]byte, 1<<10)
		n, err := c.Read(buf)
		if err != nil {
			fmt.Println(err)
			break
		}
		fmt.Printf("received %d bytes, data: %s", n, string(buf))
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGKILL, syscall.SIGINT)
	<-sig
}

func receiveListener(conn *net.UnixConn) net.Listener {
	connFd, err := getConnFd(conn)
	if err != nil {
		panic(err)
	}

	// receive socket control message
	b := make([]byte, syscall.CmsgSpace(4))
	_, _, _, _, err = syscall.Recvmsg(connFd, nil, b, 0)
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
	log.Printf("Got socket fd %d\n", fd)

	// construct net listener
	f := os.NewFile(uintptr(fd), "listener")
	//defer f.Close()

	l, err := net.FileListener(f)
	if err != nil {
		panic(err)
	}
	return l
}

func receiveConn(conn *net.UnixConn) net.Conn {
	connFd, err := getConnFd(conn)
	if err != nil {
		panic(err)
	}

	// receive socket control message
	b := make([]byte, syscall.CmsgSpace(4))
	_, _, _, _, err = syscall.Recvmsg(connFd, nil, b, 0)
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
	log.Printf("Got socket fd %d\n", fd)

	// construct net listener
	f := os.NewFile(uintptr(fd), "listener")
	//defer f.Close()

	l, err := net.FileConn(f)
	if err != nil {
		panic(err)
	}
	//c, err := l.Accept()
	//fmt.Println("error accept on old socket:", err)

	return l
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
				//c.Close()
				//f.Close()
				//fmt.Printf("removing %s from cache\n", f.Name())
				//delete(cache, f.Name())
			}()

			fmt.Printf("adding %s to cache\n", f.Name())
			//cache[f.Name()] = f.Fd()

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
