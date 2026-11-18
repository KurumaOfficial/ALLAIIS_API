// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"
)

func main() {
	port := flag.Int("port", 8430, "loopback port (0 chooses a free port)")
	flag.Parse()

	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(*port)))
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	host := listener.Addr().String()
	apiKey, workerKey := rand.Text(), rand.Text()

	fmt.Println("Browser:", "http://"+host+"/")
	fmt.Println("Upstream base URL:", "http://"+host+"/v1")
	fmt.Println("Upstream API key:", apiKey)
	fmt.Println("Browser worker code:", workerKey)

	server := http.Server{
		Handler: newBridge(host, apiKey, workerKey, 20*time.Second), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
	log.Fatal(server.Serve(listener))
}
