package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const udsPath = "/tmp/transfer.sock"

// upgrade service
func main() {
	syscall.Unlink(udsPath)
	transferListener, err := net.Listen("unix", udsPath)
	if err != nil {
		panic(err)
	}
	//defer transferListener.Close()

	// accept in goroutine, we want accept to start before sending signal to avoid race condition
	// call wg done when connection comes in to continue processing
	wg := sync.WaitGroup{}
	wg.Add(1)
	var conn net.Conn
	go func() {
		defer wg.Done()
		defer transferListener.Close()
		conn, err = transferListener.Accept()
		if err != nil {
			panic(err)
		}
		//defer conn.Close()
	}()

	log.Println("Sending USR2 signal to transfer service")
	cmd := exec.Command("systemctl", "kill", "--signal=USR2", "transfer.service")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Start()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Start: "+err.Error())
		return
	}

	log.Println("Wait receiving listener ...")
	wg.Wait()

	// returns the first and only 1 fd received from transfer service over unix socket
	// c is the reference to the conn of the established socket from other service
	c := receiveConn(conn.(*net.UnixConn)) // this is a net.Conn

	// need to wait for a signal here
	sig := make(chan os.Signal, 1)

	signal.Notify(sig, syscall.SIGUSR1)

	fmt.Println("wait for USR1 signal")
	<-sig
	fmt.Println("received USR1 signal")
	transferListener.Close()

	fmt.Println("send socket")
	time.Sleep(2 * time.Second)
	err = sendSocket(conn.(*net.UnixConn), c.(*net.TCPConn))
	if err != nil {
		fmt.Println(err)
		return
	}

	conn.Close()

	time.Sleep(2 * time.Second)
	fmt.Println("Upgrade service complete")
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
	log.Printf("Got socket fd %d\n", fd)

	// construct net conn
	f := os.NewFile(uintptr(fd), "conn")
	defer f.Close()

	l, err := net.FileConn(f)
	if err != nil {
		panic(err)
	}

	return l
}

func sendSocket(cc *net.UnixConn, fd *net.TCPConn) error {
	// connect to the unix socket
	conn, err := net.Dial("unix", udsPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	time.Sleep(5 * time.Second)
	fmt.Println("get conn fd")
	connFd, err := getConnFd(conn.(*net.UnixConn))
	if err != nil {
		return err
	}

	var fds []uintptr

	fmt.Println("get fd file")
	f, err := fd.File()
	if err != nil {
		fmt.Println(err)
		return err
	}

	fmt.Println("collect fds")
	fds = append(fds, f.Fd())

	rights := syscall.UnixRights(int(fds[0]))
	fmt.Println("sendmsg")
	return syscall.Sendmsg(connFd, nil, rights, nil, 0)
}
