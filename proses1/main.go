package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	memSize  = 64
	halfSize = memSize / 2
)

func main() {
	// 1. Buat Anonim Shared Memory via MemfdCreate
	shmFD, err := unix.MemfdCreate("shm", unix.MFD_CLOEXEC)
	if err != nil {
		log.Fatalln(err)
	}
	defer unix.Close(shmFD)

	if err := unix.Ftruncate(shmFD, memSize); err != nil {
		log.Fatalln(err)
	}

	shmData, err := unix.Mmap(shmFD, 0, memSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		log.Fatalln(err)
	}
	defer unix.Munmap(shmData)

	// Pisahkan area memori menjadi 2 bagian
	p1WriteArea := shmData[:halfSize]
	p1ReadArea := shmData[halfSize:]

	// 2. Buat 2 buah Eventfd untuk komunikasi dua arah
	evfd1, _ := unix.Eventfd(0, unix.EFD_CLOEXEC) // Sinyal ke Proses 2
	evfd2, _ := unix.Eventfd(0, unix.EFD_CLOEXEC) // Sinyal ke Proses 1
	defer unix.Close(evfd1)
	defer unix.Close(evfd2)

	// 3. Setup Unix Socket Listener
	socketPath := "../memfd_2way.sock"
	os.Remove(socketPath)
	addr, _ := net.ResolveUnixAddr("unix", socketPath)
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		log.Fatalln(err)
	}
	defer listener.Close()

	fmt.Println("[proses1] siap. Silakan jalankan [proses2] di terminal lain...")
	unixConn, err := listener.AcceptUnix()
	if err != nil {
		log.Fatalln(err)
	}
	defer unixConn.Close()

	rawFile, _ := unixConn.File()
	socketFD := int(rawFile.Fd())

	// 4. Oper 3 File Descriptor sekaligus (Memfd, Evfd1, Evfd2) lewat kernel
	rights := unix.UnixRights(shmFD, evfd1, evfd2)
	if err = unix.Sendmsg(socketFD, nil, rights, nil, 0); err != nil {
		log.Fatalln(err)
	}
	fmt.Println("[shmFD, evfd1 & evfd2] terkirim secara aman.")

	go func(p1WriteArea []byte, evfd1 int) {
		var signalVal uint64 = 1
		signalBytes := (*[8]byte)(unsafe.Pointer(&signalVal))[:]
		for {
			fmt.Println("👉Tulis pesan, lalu [ENTER]:")
			length, _ := os.Stdin.Read(p1WriteArea[1:])
			p1WriteArea[0] = byte(length)
			fmt.Println("[proses1] data ditulis ke RAM.")

			// Picu evfd1 untuk membangunkan Proses 2
			_, _ = unix.Write(evfd1, signalBytes)
		}
	}(p1WriteArea, evfd1)

	var sigBuf [8]byte
	for {
		/*
			err = waitForIO(evfd2, unix.EPOLLIN, 30*time.Second)
			if err != nil {
				log.Fatalf("[proses1] Timeout/Gagal menunggu balasan dari P2: %v", err)
			}
		*/
		_, _ = unix.Read(evfd2, sigBuf[:])

		// Baca pesan dari area baca RAM
		length := p1ReadArea[0] + 1
		fmt.Printf("📬Pesan diterima:\n%s\n👉Tulis pesan, lalu [ENTER]:\n", string(p1ReadArea[1:length]))
	}
}
func waitForIO(fd int, events uint32, timeout time.Duration) error {
	epfd, err := unix.EpollCreate1(0)
	if err != nil {
		return err
	}
	defer unix.Close(epfd)

	event := unix.EpollEvent{Events: events, Fd: int32(fd)}
	if err := unix.EpollCtl(epfd, unix.EPOLL_CTL_ADD, fd, &event); err != nil {
		return err
	}

	epollEvents := make([]unix.EpollEvent, 1)
	n, err := unix.EpollWait(epfd, epollEvents, int(timeout.Milliseconds()))
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("timeout setelah %v", timeout)
	}
	return nil
}
