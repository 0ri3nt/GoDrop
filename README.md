# GoDrop

Peer-to-peer LAN file sharing in Go. No accounts, no cloud, no typing
in IP addresses — GoDrop finds other instances on your network
automatically and sends files directly between them, encrypted.

## Features

- 🔍 **Auto-discovery** — finds other GoDrop instances on your LAN via mDNS (like AirDrop)
- 🔒 **Encrypted transfers** — every file goes over TLS
- ✅ **Integrity checked** — SHA-256 verified on arrival, so you know it arrived intact
- 🌐 **Web UI** — see peers and transfer progress live, right in your browser

## Getting started

Requires Go 1.22+.

```bash
git clone https://github.com/0ri3nt/GoDrop.git
cd GoDrop
go mod tidy
go build -o godrop ./cmd/godrop
./godrop --name "My Laptop"
```

Open **http://localhost:7777**. Run it on another device on the same
wifi and it'll show up automatically within a few seconds.

**Flags:**

| Flag | Default | What it does |
|---|---|---|
| `--http-port` | `7777` | Web UI port |
| `--xfer-port` | auto | TLS transfer port |
| `--name` | hostname | Name shown to other peers |
| `--download-dir` | `~/Downloads/GoDrop` | Where received files go |

## How it works

```
Discover peers (mDNS)  →  Connect (TLS)  →  Offer & accept  →  Send file  →  Verify checksum
```

1. GoDrop announces itself on the network and listens for other
   instances doing the same — no server required.
2. To send a file, it opens a direct, encrypted connection to that peer.
3. The peer sees a preview (filename + size) and can accept or reject.
4. If accepted, the file streams over, and both sides confirm it
   arrived byte-for-byte intact via a checksum.

## Heads up: the security trade-off

There's no certificate authority on a home network, so GoDrop trusts
whichever peer it's talking to rather than verifying identities like a
website would. That means transfers are private from anyone just
listening in, but not from someone actively impersonating a peer on
your network. Fine for your own devices at home — don't run this on
public wifi.

## Optional: let non-GoDrop devices join

Run with `--allow-guests` (on by default) and GoDrop prints a link
like `http://192.168.1.42:7777/guest` — open that on any phone or
laptop's browser, no install needed, to upload/download files through
the host. You approve or reject everything that comes in.

Turn it off with `--allow-guests=false` if you're on a network you
don't fully trust.

## Roadmap / known gaps

- No resume for interrupted transfers
- One file per transfer (no folders/batches yet)
- Restarting GoDrop briefly shows you as a duplicate peer to others (~45s)
- The in-browser file picker can't grab a real file path yet — see `internal/relay/` if you want to help finish this