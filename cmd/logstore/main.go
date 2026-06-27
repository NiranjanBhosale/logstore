package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	logstore "github.com/NiranjanBhosale/logstore/internal/log"
)

func main() {
	dir := filepath.Join(".", "data")
	defer os.RemoveAll(dir)

	fmt.Println("=== Session 1: Create log and write records ===")
	session1(dir)

	fmt.Println("\n=== Session 2: Reopen and read everything back ===")
	session2(dir)

	fmt.Println("\n=== Session 3: Reopen, append more, read all ===")
	session3(dir)

	fmt.Println("\n=== Session 4: Small segments to show rotation ===")
	rotationDemo()

	fmt.Println("\nDone.")
}

func session1(dir string) {
	l, err := logstore.NewLog(dir, 0, logstore.SyncConfig{Mode: logstore.SyncNone})
	if err != nil {
		log.Fatalf("create log: %v", err)
	}

	records := []string{
		"user:1001 action:login",
		"user:1002 action:upload file:backup.tar.gz",
		"user:1001 action:logout",
	}

	for _, r := range records {
		offset, err := l.Append([]byte(r))
		if err != nil {
			log.Fatalf("append: %v", err)
		}
		fmt.Printf("  appended offset %d: %q\n", offset, r)
	}

	fmt.Printf("  total size: %d bytes\n", l.Size())

	if err := l.Close(); err != nil {
		log.Fatalf("close: %v", err)
	}
	fmt.Println("  closed log")
}

func session2(dir string) {
	l, err := logstore.NewLog(dir, 0, logstore.SyncConfig{Mode: logstore.SyncNone})
	if err != nil {
		log.Fatalf("reopen log: %v", err)
	}

	fmt.Printf("  reopened log, size: %d bytes\n", l.Size())

	for i := uint64(0); ; i++ {
		data, err := l.Read(i)
		if err != nil {
			fmt.Printf("  total records found: %d\n", i)
			break
		}
		fmt.Printf("  read offset %d: %q\n", i, string(data))
	}

	if err := l.Close(); err != nil {
		log.Fatalf("close: %v", err)
	}
}

func session3(dir string) {
	l, err := logstore.NewLog(dir, 0, logstore.SyncConfig{Mode: logstore.SyncNone})
	if err != nil {
		log.Fatalf("reopen log: %v", err)
	}

	newRecords := []string{
		"user:1003 action:login",
		"user:1003 action:download file:report.pdf",
	}

	for _, r := range newRecords {
		offset, err := l.Append([]byte(r))
		if err != nil {
			log.Fatalf("append: %v", err)
		}
		fmt.Printf("  appended offset %d: %q\n", offset, r)
	}

	fmt.Println("  --- all records ---")
	for i := uint64(0); ; i++ {
		data, err := l.Read(i)
		if err != nil {
			fmt.Printf("  total records: %d\n", i)
			break
		}
		fmt.Printf("  [%d] %s\n", i, string(data))
	}

	if err := l.Close(); err != nil {
		log.Fatalf("close: %v", err)
	}
}

func rotationDemo() {
	dir := filepath.Join(".", "data-rotation")
	defer os.RemoveAll(dir)

	// 50 bytes per segment forces frequent rotation.
	l, err := logstore.NewLog(dir, 50, logstore.SyncConfig{Mode: logstore.SyncNone})
	if err != nil {
		log.Fatalf("create log: %v", err)
	}

	for i := 0; i < 8; i++ {
		data := fmt.Sprintf("record-%d", i)
		offset, err := l.Append([]byte(data))
		if err != nil {
			log.Fatalf("append: %v", err)
		}
		fmt.Printf("  appended offset %d: %q\n", offset, data)
	}

	fmt.Printf("  total size: %d bytes\n", l.Size())

	// Show segment files.
	entries, _ := os.ReadDir(dir)
	fmt.Println("  segment files:")
	for _, e := range entries {
		info, _ := e.Info()
		fmt.Printf("    %s (%d bytes)\n", e.Name(), info.Size())
	}

	// Read all records back.
	fmt.Println("  --- reading all records ---")
	for i := 0; i < 8; i++ {
		got, err := l.Read(uint64(i))
		if err != nil {
			log.Fatalf("read %d: %v", i, err)
		}
		fmt.Printf("  [%d] %s\n", i, string(got))
	}

	if err := l.Close(); err != nil {
		log.Fatalf("close: %v", err)
	}
}
