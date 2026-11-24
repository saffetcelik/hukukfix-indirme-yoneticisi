// dltest downloads one URL with the engine into a folder and reports what
// happened — a quick way to try a real link (file or HLS stream) without the app.
//
//	go run ./tools/dltest <output-folder> <url> [quality]
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"hukukfixdm/internal/engine"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: dltest <output-folder> <url> [quality]")
		os.Exit(2)
	}
	out, _ := filepath.Abs(os.Args[1])
	quality := 0
	if len(os.Args) > 3 {
		quality, _ = strconv.Atoi(os.Args[3])
	}
	data, err := os.MkdirTemp("", "hfdm-dltest-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(data)
	e, err := engine.New(data)
	if err != nil {
		panic(err)
	}
	s := e.Settings()
	s.DownloadDir = out
	e.SetSettings(s)
	e.Start()
	defer e.Close()

	snap, err := e.Add(engine.AddRequest{URL: os.Args[2], Quality: quality})
	if err != nil {
		fmt.Println("rejected:", err)
		os.Exit(1)
	}
	start := time.Now()
	last := time.Now()
	for {
		time.Sleep(200 * time.Millisecond)
		d, _ := e.Get(snap.ID)
		if time.Since(last) > 2*time.Second {
			last = time.Now()
			fmt.Printf("  %5.1fs %-11s %7.1f / %7.1f MB  %5.1f MB/s  conns=%d\n", time.Since(start).Seconds(), d.State,
				float64(d.Received)/1048576, float64(d.Total)/1048576, d.Speed/1048576, d.Conns)
		}
		switch d.State {
		case engine.StateCompleted:
			fmt.Printf("OK kind=%q file=%q size=%d bytes in %.1fs\n", d.Kind, d.Path, d.Total, time.Since(start).Seconds())
			return
		case engine.StateFailed:
			fmt.Printf("FAILED %s %s\n", d.ErrorCode, d.ErrorDetail)
			os.Exit(1)
		}
	}
}
