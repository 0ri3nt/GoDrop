// Package protocol defines the wire format used between GoDrop peers.
//
// Every transfer starts with a length-prefixed JSON "FileOffer" message,
// the receiving side replies with a length-prefixed JSON "OfferResponse",
// and if accepted, the raw file bytes follow immediately after, exactly
// Size bytes long. This keeps the protocol trivial to reason about and
// easy to extend later (e.g. multi-file batches, resume tokens).
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// ServiceName is the mDNS service type GoDrop instances advertise/browse.
const ServiceName = "_godrop._tcp"

// ProtocolVersion lets peers reject incompatible versions early.
const ProtocolVersion = 1

// FileOffer is sent by the sender immediately after the TLS handshake
// completes, describing the file that will follow if accepted.
type FileOffer struct {
	Version  int    `json:"version"`
	Name     string `json:"name"`     // filename, base name only (no path)
	Size     int64  `json:"size"`     // bytes
	SHA256   string `json:"sha256"`   // hex-encoded checksum of the file
	SenderID string `json:"senderId"` // sender's peer ID (uuid)
}

// OfferResponse is the receiver's reply to a FileOffer.
type OfferResponse struct {
	Accept bool   `json:"accept"`
	Reason string `json:"reason,omitempty"` // populated when Accept == false
}

// TransferResult is sent by the receiver after it has finished reading
// the file bytes, confirming success or reporting a failure (e.g.
// checksum mismatch).
type TransferResult struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	SHA256 string `json:"sha256"` // checksum the receiver actually computed
}

// WriteMessage writes a length-prefixed JSON message to w.
// Frame format: 4-byte big-endian uint32 length, followed by that many
// bytes of JSON.
func WriteMessage(w io.Writer, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("protocol: marshal: %w", err)
	}
	if len(data) > 10*1024*1024 {
		// Metadata messages should always be tiny; this guards against
		// a misbehaving peer trying to make us allocate absurd buffers.
		return fmt.Errorf("protocol: message too large (%d bytes)", len(data))
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("protocol: write length prefix: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("protocol: write payload: %w", err)
	}
	return nil
}

// ReadMessage reads a length-prefixed JSON message from r into v.
func ReadMessage(r io.Reader, v interface{}) error {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return fmt.Errorf("protocol: read length prefix: %w", err)
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	const maxMsgSize = 10 * 1024 * 1024
	if n > maxMsgSize {
		return fmt.Errorf("protocol: declared message size %d exceeds limit", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return fmt.Errorf("protocol: read payload: %w", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("protocol: unmarshal: %w", err)
	}
	return nil
}