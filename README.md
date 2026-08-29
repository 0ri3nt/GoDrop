# GoDrop

A simple LAN P2P file sharing utility written in Go.

## Overview
GoDrop discovers peers on the local network via UDP broadcast and serves files over HTTP so peers can browse and download files from each other.

## Repository layout
- index.html — simple upload UI (root server)
- index2.html — P2P discovery UI (peer server)
- main.go — single-server uploader/host (root mode)
- server/main.go — peer-discovery + file host (P2P mode)
- LICENSE
- go.mod

## Features
- UDP-based LAN peer discovery (port 9999)
- HTTP file listing and downloads (port 8080)
- Small web UIs for uploading and browsing peers
- CORS enabled for browser-to-browser downloads

## Configuration (server/main.go)
- FilePort: TCP port for HTTP (default 8080)
- DiscoveryPort: UDP port for discovery (default 9999)
- SharedFolder: folder served by the peer server (default ./shared)

## Quick start (Windows)
1. Single-server uploader (root mode):
    - Run: go run main.go
    - Open: http://localhost:8080

2. Peer-discovery server (P2P mode):
    - Run: go run ./server
    - Open: http://localhost:8080

## Endpoints
- GET /         — UI (index2.html for peer server)
- GET /peers    — JSON list of discovered peers
- GET /files    — JSON list of files in shared folder
- GET /download/<file> — Serve files from the shared folder

## Notes
- server/main.go ensures ./shared exists.
- Discovery uses UDP broadcast; allow UDP 9999 and TCP 8080 in your firewall for LAN use.
- Peers are considered stale after ~10s of silence and are removed from the list.

## Troubleshooting
- If peers do not appear, check firewall/antivirus and allow UDP broadcasts on your LAN.
- Inspect server/main.go for discovery and HTTP handler behavior.