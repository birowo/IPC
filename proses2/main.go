package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	memSize  = 64
	halfSize = memSize / 2
)

func main() {
	socketPath := "../memfd_2way.sock"
	addr, _ := net.ResolveUnixAddr("unix", socketPath)
	unixConn, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		log.Fatalln(err)
	}
	defer unixConn.Close()

	rawFile, _ := unixConn.File()
	socketFD := int(rawFile.Fd())

	// Siapkan ruang untuk menampung 3 FD (3 * 4 byte = 12 byte)
	oob := make([]byte, unix.CmsgSpace(12))
	dummyBuf := []byte{}

	_, oobn, _, _, err := unix.Recvmsg(socketFD, dummyBuf, oob, 0)
	if err != nil {
		log.Fatalln(err)
	}

	cmsgs, _ := unix.ParseSocketControlMessage(oob[:oobn])
	fds, err := unix.ParseUnixRights(&cmsgs[0])
	if err != nil || len(fds) < 3 {
		log.Fatalln(err)
	}

	sharedMemFD := fds[0]
	evfd1 := fds[1] // Untuk menerima sinyal masuk
	evfd2 := fds[2] // Untuk mengirim balik sinyal
	defer unix.Close(sharedMemFD)
	defer unix.Close(evfd1)
	defer unix.Close(evfd2)

	// Petakan memfd ke ruang alamat RAM milik Proses 2
	shmData, err := unix.Mmap(sharedMemFD, 0, memSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		log.Fatalf("Gagal mmap Proses 2: %v", err)
	}
	defer unix.Munmap(shmData)

	// Sisi Proses 2 terbalik: Membaca dari bagian pertama, menulis ke bagian kedua
	p2ReadArea := shmData[:halfSize]
	p2WriteArea := shmData[halfSize:]

	fmt.Println("[proses2] Sistem Standby.Menunggu lonceng evfd1 berbunyi...")
	go func(p2ReadArea []byte, evfd1 int) {
		var sigBuf [8]byte
		for {
			// 1. Tidur hemat CPU sampai dibangunkan oleh Proses 1 via evfd1
			_, _ = unix.Read(evfd1, sigBuf[:])
			// 2. Baca data dari RAM area pertama
			length := p2ReadArea[0] + 1
			fmt.Printf("📥 Pesan diterima:\n%s\nTulis pesan lalu [ENTER]:\n", string(p2ReadArea[1:length]))
		}
	}(p2ReadArea, evfd1)
	var signalVal uint64 = 1
	signalBytes := (*[8]byte)(unsafe.Pointer(&signalVal))[:]
	for {
		// 3. Tulis balasan ke RAM area kedua (Zero-copy)
		fmt.Println("Tulis pesan lalu [ENTER]:")
		length, _ := os.Stdin.Read(p2WriteArea[1:])
		p2WriteArea[0] = byte(length)
		fmt.Println("[proses2] pesan ditulis ke RAM.")

		// 4. Memicu evfd2 untuk memberi tahu Proses 1 bahwa pesan sudah siap
		_, _ = unix.Write(evfd2, signalBytes)
	}
}
