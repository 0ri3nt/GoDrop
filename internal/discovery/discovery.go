// Package discovery handles mDNS advertisement and browsing so GoDrop
// instances can find each other on the same LAN without any manual
// configuration or central server.
package discovery

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"

	"godrop/internal/protocol"
)

// Peer represents another GoDrop instance discovered on the network.
type Peer struct {
	ID       string // stable peer UUID, from the TXT record
	Name     string // human-friendly display name (hostname etc.)
	Host     string // resolved IP to dial
	Port     int
	LastSeen time.Time
}

// Registry tracks currently-known peers and notifies subscribers when
// the peer set changes, so the web UI can push live updates.
type Registry struct {
	mu       sync.RWMutex
	peers    map[string]Peer // keyed by Peer.ID
	onChange func([]Peer)
}

func NewRegistry(onChange func([]Peer)) *Registry {
	return &Registry{
		peers:    make(map[string]Peer),
		onChange: onChange,
	}
}

func (r *Registry) upsert(p Peer) {
	r.mu.Lock()
	r.peers[p.ID] = p
	snapshot := r.snapshotLocked()
	r.mu.Unlock()
	if r.onChange != nil {
		r.onChange(snapshot)
	}
}

func (r *Registry) remove(id string) {
	r.mu.Lock()
	delete(r.peers, id)
	snapshot := r.snapshotLocked()
	r.mu.Unlock()
	if r.onChange != nil {
		r.onChange(snapshot)
	}
}

func (r *Registry) snapshotLocked() []Peer {
	out := make([]Peer, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, p)
	}
	return out
}

// Snapshot returns a copy of the currently known peers.
func (r *Registry) Snapshot() []Peer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshotLocked()
}

// Get looks up a peer by ID.
func (r *Registry) Get(id string) (Peer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.peers[id]
	return p, ok
}

// Advertiser publishes this instance's presence via mDNS.
type Advertiser struct {
	server *zeroconf.Server
}

// Advertise registers this instance on the LAN under protocol.ServiceName.
// selfID is a stable UUID for this instance; displayName is shown to
// other users (e.g. "Agni's Laptop"); port is the TLS transfer port.
func Advertise(selfID, displayName string, port int) (*Advertiser, error) {
	txt := []string{
		"id=" + selfID,
		"name=" + displayName,
		fmt.Sprintf("v=%d", protocol.ProtocolVersion),
	}
	server, err := zeroconf.Register(
		displayName,          // instance name shown in service discovery
		protocol.ServiceName, // service type
		"local.",             // domain
		port,
		txt,
		nil, // advertise on all available network interfaces
	)
	if err != nil {
		return nil, fmt.Errorf("discovery: register mdns service: %w", err)
	}
	return &Advertiser{server: server}, nil
}

// Shutdown stops advertising this instance.
func (a *Advertiser) Shutdown() {
	if a.server != nil {
		a.server.Shutdown()
	}
}

// Browse continuously scans for other GoDrop instances until ctx is
// canceled, feeding discovered/updated peers into the given Registry.
// selfID is excluded so we never list ourselves as a peer.
func Browse(ctx context.Context, selfID string, registry *Registry) error {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return fmt.Errorf("discovery: create resolver: %w", err)
	}

	entries := make(chan *zeroconf.ServiceEntry, 16)

	go func() {
		for entry := range entries {
			peer, ok := parseEntry(entry)
			if !ok || peer.ID == selfID {
				continue
			}
			registry.upsert(peer)
		}
	}()

	if err := resolver.Browse(ctx, protocol.ServiceName, "local.", entries); err != nil {
		return fmt.Errorf("discovery: browse: %w", err)
	}

	// Periodically prune peers we haven't seen in a while, since mDNS
	// doesn't reliably notify us of graceful/ungraceful peer departure.
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pruneStale(registry, 45*time.Second)
		}
	}
}

func pruneStale(registry *Registry, maxAge time.Duration) {
	now := time.Now()
	for _, p := range registry.Snapshot() {
		if now.Sub(p.LastSeen) > maxAge {
			registry.remove(p.ID)
		}
	}
}

// parseEntry converts a raw zeroconf.ServiceEntry into our Peer type,
// pulling the peer ID and display name out of the TXT record.
func parseEntry(entry *zeroconf.ServiceEntry) (Peer, bool) {
	var id, name string
	for _, kv := range entry.Text {
		switch {
		case len(kv) > 3 && kv[:3] == "id=":
			id = kv[3:]
		case len(kv) > 5 && kv[:5] == "name=":
			name = kv[5:]
		}
	}
	if id == "" {
		log.Printf("discovery: ignoring mdns entry with no id: %+v", entry)
		return Peer{}, false
	}
	if name == "" {
		name = entry.Instance
	}

	host := ""
	if len(entry.AddrIPv4) > 0 {
		host = entry.AddrIPv4[0].String()
	} else if len(entry.AddrIPv6) > 0 {
		host = entry.AddrIPv6[0].String()
	}
	if host == "" {
		return Peer{}, false
	}

	return Peer{
		ID:       id,
		Name:     name,
		Host:     host,
		Port:     entry.Port,
		LastSeen: time.Now(),
	}, true
}