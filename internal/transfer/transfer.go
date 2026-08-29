// Package transfer implements the actual file transfer over a
// TLS-encrypted TCP connection, once a peer has been found via discovery.
package transfer

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"godrop/internal/protocol"
)

// IncomingTransfer describes a transfer request the UI needs to
// approve or reject, plus the machinery to act on that decision.
type IncomingTransfer struct {
	Offer      protocol.FileOffer
	RemoteAddr string

	respond  chan<- bool // true = accept, false = reject
	fileData io.Reader   // exactly Offer.Size bytes, must be fully consumed on accept
	done     chan<- error
}

// Accept tells the server to proceed with receiving the file, writing it
// into destDir. It blocks until the transfer completes (or fails) and
// returns the resulting error, if any.
func (t *IncomingTransfer) Accept(destDir string) error {
	t.respond <- true
	return t.receiveAndSave(destDir)
}

// Reject tells the sender the offer was declined.
func (t *IncomingTransfer) Reject() {
	t.respond <- false
}

func (t *IncomingTransfer) receiveAndSave(destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		err = fmt.Errorf("transfer: create dest dir: %w", err)
		t.done <- err
		return err
	}

	safeName := filepath.Base(t.Offer.Name) // never trust a path from the peer
	destPath := filepath.Join(destDir, safeName)

	f, err := os.Create(destPath)
	if err != nil {
		err = fmt.Errorf("transfer: create file: %w", err)
		t.done <- err
		return err
	}
	defer f.Close()

	hasher := sha256.New()
	mw := io.MultiWriter(f, hasher)

	if _, err := io.CopyN(mw, t.fileData, t.Offer.Size); err != nil {
		err = fmt.Errorf("transfer: write file data: %w", err)
		t.done <- err
		return err
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	if sum != t.Offer.SHA256 {
		err = fmt.Errorf("transfer: checksum mismatch: got %s want %s", sum, t.Offer.SHA256)
		t.done <- err
		return err
	}

	t.done <- nil
	return nil
}

// Server listens for incoming GoDrop transfers.
type Server struct {
	listener net.Listener
	incoming chan *IncomingTransfer
}

// Listen starts a TLS listener on the given port using cert, and returns
// a Server whose Incoming() channel yields transfer requests for the UI
// to accept or reject. port=0 lets the OS choose a free port; call
// Server.Port() afterwards to find out which one.
func Listen(port int, cert tls.Certificate) (*Server, error) {
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	ln, err := tls.Listen("tcp", fmt.Sprintf(":%d", port), cfg)
	if err != nil {
		return nil, fmt.Errorf("transfer: listen: %w", err)
	}
	s := &Server{
		listener: ln,
		incoming: make(chan *IncomingTransfer),
	}
	go s.acceptLoop()
	return s, nil
}

// Port returns the actual TCP port this server is bound to.
func (s *Server) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

// Incoming yields a request for every transfer attempt; the caller must
// call Accept or Reject on each one.
func (s *Server) Incoming() <-chan *IncomingTransfer {
	return s.incoming
}

// Close stops accepting new connections.
func (s *Server) Close() error {
	return s.listener.Close()
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// Listener closed; exit quietly.
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Minute))

	var offer protocol.FileOffer
	if err := protocol.ReadMessage(conn, &offer); err != nil {
		return // malformed handshake, drop silently
	}
	if offer.Version != protocol.ProtocolVersion {
		protocol.WriteMessage(conn, protocol.OfferResponse{
			Accept: false,
			Reason: "protocol version mismatch",
		})
		return
	}

	respond := make(chan bool)
	done := make(chan error, 1)
	it := &IncomingTransfer{
		Offer:      offer,
		RemoteAddr: conn.RemoteAddr().String(),
		respond:    respond,
		fileData:   conn,
		done:       done,
	}

	s.incoming <- it // hand off to UI/caller; blocks until someone reads it

	accepted := <-respond
	if err := protocol.WriteMessage(conn, protocol.OfferResponse{Accept: accepted}); err != nil {
		return
	}
	if !accepted {
		return
	}

	// Give large transfers more room than the handshake deadline.
	conn.SetDeadline(time.Now().Add(30 * time.Minute))

	err := <-done // set by receiveAndSave once the file is fully written
	result := protocol.TransferResult{OK: err == nil}
	if err != nil {
		result.Error = err.Error()
	} else {
		result.SHA256 = offer.SHA256
	}
	protocol.WriteMessage(conn, result)
}

// SendFile connects to a peer at host:port and sends filePath, reporting
// progress via onProgress(bytesSent, totalBytes). cert is this instance's
// own TLS certificate (used as the client cert; the peer's server cert
// is trusted on the LAN-local trust model — see certutil).
func SendFile(host string, port int, filePath string, cert tls.Certificate, onProgress func(sent, total int64)) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("transfer: open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("transfer: stat file: %w", err)
	}

	// Hash the file up front so the receiver can verify integrity.
	// For very large files this means reading the file twice (once to
	// hash, once to send); an optimization for later would be streaming
	// through a TeeReader and hashing while sending, accepting that a
	// checksum mismatch is then only detectable after the fact.
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return fmt.Errorf("transfer: hash file: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("transfer: rewind file: %w", err)
	}
	checksum := hex.EncodeToString(hasher.Sum(nil))

	addr := fmt.Sprintf("%s:%d", host, port)
	dialCfg := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, // trust-on-first-use LAN model; see certutil
		MinVersion:         tls.VersionTLS12,
	}
	conn, err := tls.Dial("tcp", addr, dialCfg)
	if err != nil {
		return fmt.Errorf("transfer: dial %s: %w", addr, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Minute))

	offer := protocol.FileOffer{
		Version: protocol.ProtocolVersion,
		Name:    filepath.Base(filePath),
		Size:    info.Size(),
		SHA256:  checksum,
	}
	if err := protocol.WriteMessage(conn, offer); err != nil {
		return fmt.Errorf("transfer: send offer: %w", err)
	}

	var resp protocol.OfferResponse
	if err := protocol.ReadMessage(conn, &resp); err != nil {
		return fmt.Errorf("transfer: read offer response: %w", err)
	}
	if !resp.Accept {
		return fmt.Errorf("transfer: peer declined transfer: %s", resp.Reason)
	}

	conn.SetDeadline(time.Now().Add(30 * time.Minute))

	var sent int64
	buf := make([]byte, 32*1024)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if _, werr := conn.Write(buf[:n]); werr != nil {
				return fmt.Errorf("transfer: write file data: %w", werr)
			}
			sent += int64(n)
			if onProgress != nil {
				onProgress(sent, info.Size())
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("transfer: read file data: %w", rerr)
		}
	}

	var result protocol.TransferResult
	if err := protocol.ReadMessage(conn, &result); err != nil {
		return fmt.Errorf("transfer: read result: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("transfer: receiver reported failure: %s", result.Error)
	}
	return nil
}