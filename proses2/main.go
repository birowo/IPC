package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	memSize  = 64
	halfSize = memSize / 2
)

func main() {
	socketPath := filepath.Join(os.TempDir(), "ipc.sock")
	addr, _ := net.ResolveUnixAddr("unix", socketPath)
	unixConn, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		log.Fatalln(err)
	}
	defer unixConn.Close()

	rawFile, _ := unixConn.File()
	defer rawFile.Close()
	socketFD := int(rawFile.Fd())

	// Siapkan ruang untuk menampung 3 FD (3 * 4 byte = 12 byte)
	oob := make([]byte, unix.CmsgSpace(12))
	_, oobn, _, _, err := unix.Recvmsg(socketFD, nil, oob, 0)
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

	// Petakan memfd ke ruang alamat RAM milik proses2
	shmData, err := unix.Mmap(sharedMemFD, 0, memSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		log.Fatalf("Gagal mmap Proses 2: %v", err)
	}
	defer unix.Munmap(shmData)

	// Sisi Proses 2 terbalik: Membaca dari bagian pertama, menulis ke bagian kedua
	readArea := shmData[:halfSize]
	writeArea := shmData[halfSize:]

	println("[proses2] Sistem Standby.Menunggu evfd1 dipicu oleh proses1")
	go func(readArea []byte, evfd1 int) {
		var sigBuf [8]byte
		for {
			// 1. Tidur hemat CPU sampai dibangunkan oleh Proses 1 via evfd1
			_, _ = unix.Read(evfd1, sigBuf[:])
			// 2. Baca data dari RAM area pertama
			length := readArea[0] + 1
			println(
				"📥 Pesan diterima:\n",
				string(readArea[1:length]),
				"\nTulis pesan lalu [ENTER]:",
			)
		}
	}(readArea, evfd1)
	var signalVal uint64 = 1
	signalBytes := (*[8]byte)(unsafe.Pointer(&signalVal))[:]
	for {
		// 3. Tulis balasan ke RAM area kedua (Zero-copy)
		fmt.Println("Tulis pesan lalu [ENTER]:")
		length, _ := os.Stdin.Read(writeArea[1:])
		writeArea[0] = byte(length)
		//[proses2] pesan ditulis ke shared memory

		// 4. Memicu evfd2 untuk memberi tahu proses1 bahwa pesan sudah siap
		_, _ = unix.Write(evfd2, signalBytes)
	}
}
