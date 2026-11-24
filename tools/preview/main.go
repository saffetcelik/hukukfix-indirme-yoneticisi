// preview serves the UI folder over HTTP so the design can be checked in a
// browser with mock data (the page falls back to a mock backend outside the app).
//
//	go run ./tools/preview [dir] [port]
package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	dir, port := "frontend/dist", "5179"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if len(os.Args) > 2 {
		port = os.Args[2]
	}
	h := http.FileServer(http.Dir(dir))
	log.Fatal(http.ListenAndServe("127.0.0.1:"+port, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})))
}
