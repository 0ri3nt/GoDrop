// Command godrop starts a GoDrop instance: it advertises itself on the
// LAN via mDNS, listens for incoming TLS file transfers, and serves a
// local web UI for picking peers and sending/receiving files.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/google/uuid"
	
	"godrop/internal/certutil"
	"godrop/internal/discovery"
	"godrop/internal/transfer"
	"godrop/internal/webui"
)

func main() {
	var (
		httpPort   = flag.Int("http-port", 7777, "port to serve the local web UI on")
		xferPort   = flag.Int("xfer-port", 0, "TCP port for TLS file transfers (0 = pick automatically)")
		name       = flag.String("name", "", "display name shown to other peers (defaults to hostname)")
		downloadTo = flag.String("download-dir", "", "directory to save received files into (defaults to ~/Downloads/GoDrop)")
	)
	flag.Parse()

	displayName := *name
	if displayName == "" {
		hn, err := os.Hostname()
		if err != nil {
			hn = "GoDrop-Device"
		}
		displayName = hn
	}

	dest := *downloadTo
	if dest == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		dest = filepath.Join(home, "Downloads", "GoDrop")
	}

	selfID := uuid.NewString()

	cert, err := certutil.GenerateSelfSigned(displayName)
	if err != nil {
		log.Fatalf("godrop: generate TLS cert: %v", err)
	}

	xferServer, err := transfer.Listen(*xferPort, cert)
	if err != nil {
		log.Fatalf("godrop: start transfer server: %v", err)
	}
	defer xferServer.Close()
	actualXferPort := xferServer.Port()

	adv, err := discovery.Advertise(selfID, displayName, actualXferPort)
	if err != nil {
		log.Fatalf("godrop: advertise on mdns: %v", err)
	}
	defer adv.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var uiServer *webui.Server
	registry := discovery.NewRegistry(func(peers []discovery.Peer) {
		if uiServer != nil {
			uiServer.PublishPeers(peers)
		}
	})

	uiServer = webui.NewServer(registry, xferServer, cert, dest)

	go func() {
		if err := discovery.Browse(ctx, selfID, registry); err != nil {
			log.Printf("godrop: browse error: %v", err)
		}
	}()

	addr := fmt.Sprintf("127.0.0.1:%d", *httpPort)
	httpServer := &http.Server{Addr: addr, Handler: uiServer.Routes()}

	go func() {
		log.Printf("GoDrop running as %q", displayName)
		log.Printf("Web UI:        http://%s", addr)
		log.Printf("Transfer port: %d (TLS)", actualXferPort)
		log.Printf("Saving received files to: %s", dest)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("godrop: http server: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Println("godrop: shutting down…")
	cancel()
	httpServer.Shutdown(context.Background())
}