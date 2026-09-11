# Reverse Tether — Cross-platform USB reverse tethering design v1.1

> This document preserves the original design and validation plan. See [README.md](README.md) for current build and usage instructions. The current iOS implementation accepts usbmux connections in the container app and forwards packets to the extension through `sendProviderMessage`; the extension listener described below is historical.

A Gnirehtet-style tool for Android and iOS, invoked as `revtether`. Devices use the computer's network connection over USB.

Changes from v1.0:

- Move iOS forward to M1 and Android to M2.
- Resolve the conflict between the tunnel subnet and LAN bypass routes by changing 10.0.0.0/24 to 198.18.0.0/24.
- Add usbmux / adb wire protocol details, gVisor integration code, and iOS / Android project configuration and skeletons.
- Add an M0 technical validation checklist covering assumptions that require physical-device testing.
- Add implementation details such as `--pcap` frame capture and a single-writer outbound queue.

---

## 1. Goals and non-goals

**Goals**

- The user opens the device app, which immediately enables the VPN and starts listening, then runs `revtether run` on the computer to use its network over USB.
- Forward the device's IPv4 traffic, including TCP, UDP, and ICMP echo, to the internet.
- Provide a single binary for macOS / Linux with no runtime dependencies other than the system `usbmuxd` service on Linux.
- Support multiple devices, including mixed platforms, at the same time.

**Non-goals for v1**

- Windows; IPv6; Wi-Fi / wireless adb debugging; upstream proxy chains (interface reserved only); launching device apps from the computer; operation without a device app.

---

## 2. Decision log

| # | Decision | Outcome |
|---|------|------|
| D1 | Relay stack | Go 1.22+, gVisor netstack (`gvisor.dev/gvisor@go` branch) |
| D2 | Connection direction | The computer connects to the device: `adb forward` for Android, usbmuxd `Connect` for iOS |
| D3 | usbmuxd integration | Implement the usbmux plist protocol directly |
| D4 | Desktop platforms | macOS (arm64 / amd64) + Linux (amd64 / arm64) |
| D5 | IPv6 | Unsupported; do not install IPv6 routes |
| D6 | DNS | The relay intercepts UDP 53 and forwards raw queries to the computer's system nameserver |
| D7 | Multiplexing | One stream with TCP window backpressure |
| D8 | UDP / ICMP | Reclaim UDP sessions after 60 s idle, with a limit of 1024 sessions; answer ICMP echo only |
| D9 | Traffic scope | Support app exclusions on Android; bypass the tunnel for LAN subnets on both platforms (RFC1918 + 169.254/16) |
| D10 | Startup | The user opens the app manually; the app enables the VPN on launch |
| D11 | Transport | USB only |
| D12 | Multiple devices | Supported, with a separate goroutine group and netstack per device |
| D13 | Upstream egress | Reserve a `Dialer` interface; use direct connections in v1 |
| D14 | MTU | 1400 |
| D15 | Tunnel subnet | `198.18.0.0/24` (RFC 2544 benchmarking range, avoiding the LAN subnets above) |

---

## 3. Architecture

```
┌─ Device ───────────────────────┐         ┌─ Host: revtether ──────────────────────────┐
│ iOS: NEPacketTunnelProvider    │         │                                            │
│   packetFlow -> frames         │         │                                            │
│   NWListener 127.0.0.1:31416   │◄─ USB ─►│ transport/usbmux  Listen/Connect(31416)    │
│                                │         │                                            │
│ Android: VpnService            │         │                                            │
│   tun fd -> frames             │         │                                            │
│   ServerSocket 127.0.0.1:31416 │◄─ USB ─►│ transport/adb     track-devices/forward    │
└────────────────────────────────┘         │                                            │
                                           │ tunnel ×N ─┬─ framing                      │
                                           │            ├─ channel.Endpoint ⇄ netstack  │
                                           │            ├─ tcp.Forwarder → Dialer       │
                                           │            ├─ udp.Forwarder → Dialer / dns │
                                           │            └─ icmp echo (before netstack)  │
                                           └────────────────────────────────────────────┘
```

The device has two responsibilities: expose system traffic as raw IP packets and listen on loopback port 31416. The computer abstracts its connection to device port 31416 as a byte stream; processing is identical for both platforms after that point.

### 3.1 Address plan

| Purpose | Address |
|------|------|
| Tunnel subnet | 198.18.0.0/24 |
| Device address | 198.18.0.2 |
| Gateway / DNS | 198.18.0.1 |

The 198.18/24 range is chosen because LAN bypass excludes 10/8, 172.16/12, 192.168/16, and 169.254/16 from the tunnel. The tunnel's own addresses must stay outside those ranges, or the exclusions would divert gateway / DNS traffic. Each device has an independent netstack, so addresses can be reused.

---

## 4. Wire protocol

A reliable byte stream between the device and computer carries a sequence of frames. All multi-byte integers are big-endian.

```
+------+--------+-----------------+
| type | length | payload         |
| u8   | u16    | length bytes    |
+------+--------+-----------------+
```

| type | Name | payload | Direction |
|------|------|---------|------|
| 0x00 | HELLO | `version u8` (=1), `platform u8` (0=android, 1=ios, 0xFF=host), `caps u16` (reserved, 0) | Bidirectional |
| 0x01 | IP | Raw IPv4 packet, 1 ≤ length ≤ 1500 | Bidirectional |
| 0x02 | KEEPALIVE | Empty | Bidirectional |

Rules:

- After the computer connects, **the device sends HELLO first**. The computer validates the version and replies with HELLO. On a mismatch, it closes the connection and prints an upgrade prompt.
- Both peers send KEEPALIVE every 5 s. Receiving no frames for 15 s means the connection has been lost.
- A length greater than 1500, an unknown type, or any frame before HELLO is a protocol error and closes the connection.
- No encryption or compression.

---

## 5. Relay core (Go)

### 5.1 Code layout

```
cmd/revtether/main.go       CLI
cmd/revtether-app/          macOS menu bar app (LSUIElement, no Dock icon or window)
internal/
  relay/run.go              Desktop main loop shared by the CLI and app
  framing/framing.go        Frame encoding and decoding
  transport/
    transport.go            Transport interface and DeviceEvent
    usbmux/usbmux.go        usbmuxd client
    adb/adb.go              adb server client
  tunnel/
    tunnel.go               Run(): handshake, stack setup, goroutines, and lifecycle
    stack.go                newStack()
    tcp.go                  TCP forwarder handler
    udp.go                  UDP forwarder handler and session count
    dns.go                  UDP 53 forwarding
    icmp.go                 echo reply
    stats.go                Counters
  dialer/dialer.go          Dialer interface and direct implementation
  resolvconf/resolvconf.go  System nameserver lookup (5 s cache)
  ui/status.go              Terminal / menu bar status
```

Dependencies: `gvisor.dev/gvisor` (go branch) and `howett.net/plist`; the macOS app also uses `fyne.io/systray`. Everything else uses the standard library.

### 5.2 Frame encoding and decoding

```go
package framing

const (
    TypeHello     byte = 0x00
    TypeIP        byte = 0x01
    TypeKeepalive byte = 0x02
    MaxPayload         = 1500
    Version       byte = 1
)

func Write(w io.Writer, t byte, p []byte) error {
    var h [3]byte
    h[0] = t
    binary.BigEndian.PutUint16(h[1:], uint16(len(p)))
    if _, err := w.Write(h[:]); err != nil { return err }
    _, err := w.Write(p)
    return err
}

// Read returns a newly allocated payload slice that the caller may retain.
func Read(r *bufio.Reader) (t byte, p []byte, err error) {
    var h [3]byte
    if _, err = io.ReadFull(r, h[:]); err != nil { return }
    n := int(binary.BigEndian.Uint16(h[1:]))
    if n > MaxPayload { return 0, nil, ErrFrameTooLarge }
    p = make([]byte, n)
    _, err = io.ReadFull(r, p)
    return h[0], p, err
}
```

### 5.3 netstack initialization

```go
package tunnel

const nicID tcpip.NICID = 1

func newStack(ep stack.LinkEndpoint) (*stack.Stack, error) {
    s := stack.New(stack.Options{
        NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
        TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
        HandleLocal:        false,
    })
    sack := tcpip.TCPSACKEnabled(true)
    s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack)
    rcv := tcpip.TCPReceiveBufferSizeRangeOption{Min: 4096, Default: 256 << 10, Max: 4 << 20}
    s.SetTransportProtocolOption(tcp.ProtocolNumber, &rcv)
    snd := tcpip.TCPSendBufferSizeRangeOption{Min: 4096, Default: 256 << 10, Max: 4 << 20}
    s.SetTransportProtocolOption(tcp.ProtocolNumber, &snd)
    mod := tcpip.TCPModerateReceiveBufferOption(true)
    s.SetTransportProtocolOption(tcp.ProtocolNumber, &mod)

    if err := s.CreateNIC(nicID, ep); err != nil { return nil, fmt.Errorf("CreateNIC: %v", err) }
    s.SetPromiscuousMode(nicID, true) // Accept any destination address.
    s.SetSpoofing(nicID, true)        // Allow replies from any source address.
    s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nicID}})
    return s, nil
}
```

Use `pkg/tcpip/link/channel` directly as the LinkEndpoint: `channel.New(256, 1400, "")`. It provides `InjectInbound` (frame → stack) and `ReadContext` (stack → frame), matching the arrangement used by wireguard-go's netstack mode.

> gVisor API names vary slightly by version; for example, older versions of `gonet.NewUDPConn` take an extra `stack` argument. Use the API from the actual go-branch commit included in the project. The `core/` directory in `xjasonlyu/tun2socks` provides a reference implementation.

### 5.4 Tunnel lifecycle and concurrency

```go
func Run(ctx context.Context, stream io.ReadWriteCloser, d dialer.Dialer, log *slog.Logger) error
```

1. **Handshake**: require HELLO as the first frame, validate the version, and reply with HELLO.
2. **Stack setup**: create `ep := channel.New(...)` and `s := newStack(ep)`, then register TCP / UDP forwarders.
3. **Goroutines** (`errgroup`; any exit cancels all of them):
   - `inbound`: `stream → decode frames → intercept ICMP / InjectInbound`.
   - `outbound`: `ep.ReadContext → out chan`.
   - `writer`: the **only writer**. Read packets from `out chan []byte` (capacity 512) and the keepalive ticker, then batch stream writes with `bufio.Writer`. Drop packets when the channel is full (`select default`) and rely on TCP retransmission.
   - `watchdog`: cancel the context if no frame arrives for 15 s.
4. **Shutdown**: cancel the context → close the stream → call `s.Close()`; wait for all goroutines to finish, then call `s.Destroy()`.

Core inbound loop:

```go
for {
    t, p, err := framing.Read(br)
    if err != nil { return err }
    lastSeen.Store(time.Now().UnixNano())
    switch t {
    case framing.TypeKeepalive:
    case framing.TypeIP:
        if reply, ok := icmpEcho(p); ok { enqueue(reply); continue }
        pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(p)})
        ep.InjectInbound(ipv4.ProtocolNumber, pkt)
        pkt.DecRef()
    default:
        return ErrProtocol
    }
}
```

Core outbound loop:

```go
for {
    pkt := ep.ReadContext(ctx)
    if pkt == nil { return nil }
    v := pkt.ToView()
    enqueue(append([]byte(nil), v.AsSlice()...))
    v.Release(); pkt.DecRef()
}
```

### 5.5 TCP forwarding

```go
fwd := tcp.NewForwarder(s, 256<<10 /*rcvWnd*/, 4096 /*maxInFlight*/, func(r *tcp.ForwarderRequest) {
    id := r.ID()
    target := net.JoinHostPort(net.IP(id.LocalAddress.AsSlice()).String(), strconv.Itoa(int(id.LocalPort)))
    dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
    hostConn, err := d.DialContext(dctx, "tcp", target)
    cancel()
    if err != nil { r.Complete(true); return } // RST lets the device observe an unreachable destination.
    var wq waiter.Queue
    ep, terr := r.CreateEndpoint(&wq)
    if terr != nil { hostConn.Close(); r.Complete(true); return }
    r.Complete(false)
    devConn := gonet.NewTCPConn(&wq, ep)
    go relayTCP(devConn, hostConn)
})
s.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)
```

`relayTCP` runs two `io.Copy` operations. On EOF in one direction, call `CloseWrite()` on the other endpoint to propagate a half-close. Close both endpoints when both directions finish. Use a 32 KB buffer and no idle timeout.

Dial before calling `CreateEndpoint` to delay SYN-ACK. The forwarder recognizes device SYN retransmissions (1 s / 2 s / 4 s) as the same in-flight request and does not dial again.

### 5.6 UDP forwarding

```go
ufwd := udp.NewForwarder(s, func(r *udp.ForwarderRequest) {
    if !sessions.TryAcquire() { return } // Drop sessions beyond the 1024-session limit.
    var wq waiter.Queue
    ep, terr := r.CreateEndpoint(&wq)
    if terr != nil { sessions.Release(); return }
    devConn := gonet.NewUDPConn(&wq, ep)
    id := r.ID()
    if id.LocalPort == 53 { go handleDNS(ctx, devConn, sessions); return }
    target := net.JoinHostPort(net.IP(id.LocalAddress.AsSlice()).String(), strconv.Itoa(int(id.LocalPort)))
    hostConn, err := d.DialContext(ctx, "udp", target)
    if err != nil { devConn.Close(); sessions.Release(); return }
    go relayUDP(devConn, hostConn, 60*time.Second, sessions)
})
s.SetTransportProtocolHandler(udp.ProtocolNumber, ufwd.HandlePacket)
```

`relayUDP` runs two read loops and updates `lastActive` on every read or write. A separate timer checks every 10 s; after 60 s idle, close both endpoints and call `Release()`.

### 5.7 DNS forwarding

The device uses 198.18.0.1 as its DNS server. All queries pass through the tunnel to destination port 53 and are routed by the UDP forwarder:

```go
func handleDNS(ctx context.Context, c *gonet.UDPConn, sess *Sessions) {
    defer func() { c.Close(); sess.Release() }()
    buf := make([]byte, 4096)
    for {
        c.SetReadDeadline(time.Now().Add(30 * time.Second))
        n, err := c.Read(buf)
        if err != nil { return }
        q := append([]byte(nil), buf[:n]...)
        go func() {
            if resp, err := forwardQuery(ctx, q); err == nil { c.Write(resp) }
        }()
    }
}

func forwardQuery(ctx context.Context, q []byte) ([]byte, error) {
    for _, ns := range resolvconf.Nameservers() {          // Cache for 5 s; fall back to 1.1.1.1 and 8.8.8.8 if empty.
        resp, err := exchangeUDP(ns, q, 3*time.Second)
        if err != nil { continue }
        if resp[2]&0x02 != 0 {                              // TC bit: retry the query over TCP.
            if r2, err := exchangeTCP(ns, q, 3*time.Second); err == nil { return r2, nil }
        }
        return resp, nil
    }
    return nil, ErrNoNameserver
}
```

No DNS semantics are parsed, so all record types and EDNS pass through unchanged. `resolvconf` reads `nameserver` lines from `/etc/resolv.conf` (which also lists the primary macOS resolver). Linux systemd-resolved's `127.0.0.53` works because the relay runs on the computer itself. Domain-specific macOS split-DNS is deferred to v2.

### 5.8 ICMP echo

Intercept packets before `InjectInbound`: for IPv4, protocol=1, and ICMP type=8, build a reply by swapping source and destination, setting type=0 and TTL=64, and recalculating the ICMP checksum (`header.ICMPv4Checksum`) and IP header checksum. Send the reply directly through `enqueue`. Drop other ICMP packets. The device can therefore ping any address successfully; this only checks tunnel liveness.

### 5.9 Dialer interface

```go
type Dialer interface {
    DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}
var Direct Dialer = &net.Dialer{Timeout: 10 * time.Second}
```

A SOCKS5 / HTTP CONNECT implementation can replace the direct dialer in v2 without changing the tunnel layer.

### 5.10 Debugging: `--pcap <file>`

The writer and inbound loop append the IP packets they handle to a PCAP file (linktype RAW=101), which Wireshark can open directly. The implementation takes about 40 lines and provides a practical way to diagnose protocol-stack issues.

---

## 6. Transport layer

### 6.1 Interface

```go
type Platform uint8
const ( Android Platform = 0; IOS Platform = 1 )

type DeviceEvent struct {
    ID       string   // usbmux DeviceID as a string, or adb serial
    Serial   string   // UDID / serial for display
    Platform Platform
    Attached bool
}

type Transport interface {
    Watch(ctx context.Context) (<-chan DeviceEvent, error)
    Connect(ctx context.Context, deviceID string, port uint16) (io.ReadWriteCloser, error)
}
```

### 6.2 iOS: usbmuxd protocol

**Connection**: Unix socket `/var/run/usbmuxd`. macOS includes usbmuxd; on Linux, install the `usbmuxd` package (`apt install usbmuxd` on Debian / Ubuntu). The socket's default mode is 0666.

**Frame format** (16-byte header, all fields little-endian):

```
u32 length     // Total length, including the header
u32 version    // = 1 (plist protocol)
u32 msgType    // = 8 (plist)
u32 tag        // Request sequence number, echoed in the response
[XML plist]
```

**Listen** (a persistent connection used by Watch):

```xml
<dict>
  <key>MessageType</key><string>Listen</string>
  <key>ClientVersionString</key><string>revtether</string>
  <key>ProgName</key><string>revtether</string>
  <key>kLibUSBMuxVersion</key><integer>3</integer>
</dict>
```

The response is `{MessageType: Result, Number: 0}`, followed by a stream of events:
- `{MessageType: Attached, DeviceID: <int>, Properties: {ConnectionType: "USB", SerialNumber: "<UDID>", ProductID, LocationID, ...}}`
- `{MessageType: Detached, DeviceID: <int>}`

Handle only devices with `ConnectionType == "USB"`. Ignore devices paired over Wi-Fi, which appear as `Network`.

**Connect** (open a new connection for each request):

```xml
<dict>
  <key>MessageType</key><string>Connect</string>
  <key>DeviceID</key><integer>5</integer>
  <key>PortNumber</key><integer>47226</integer>   <!-- Byte-swapped 31416: htons(31416) = 0xB87A = 47226 -->
  <key>ClientVersionString</key><string>revtether</string>
  <key>ProgName</key><string>revtether</string>
</dict>
```

The response is `{MessageType: Result, Number: N}`:
- `0`: success. **The socket is now a raw byte stream to device port 31416**; pass it directly to the tunnel.
- `3`: connection refused (nothing is listening on the device, so the app may not be running); retry every 2 s.
- `2`: device not found (just unplugged); stop trying.
- Other values: log the error and retry every 2 s.

**ListDevices** (used by `revtether devices`): `{MessageType: ListDevices}` → `{DeviceList: [...]}`.

**Pairing**: the device must trust the computer. macOS starts the system trust flow when the device is plugged in. On Linux, first run `idevicepair pair` from libimobiledevice-utils once. Whether an unpaired device allows Connect to an ordinary port is an M0 validation item.

### 6.3 Android: adb server protocol

**Connection**: TCP `127.0.0.1:5037`. If unreachable, run `adb start-server` once (requires adb in `PATH`) and report an error if it still fails.

**Request format**: a four-digit hexadecimal length followed by the request string, for example `001ahost:track-devices`. The response is `OKAY`, or `FAIL` followed by a four-digit hexadecimal length and error text.

**Watch**: a persistent `host:track-devices` connection. Each change sends a four-digit hexadecimal length followed by text, with one `<serial>\t<state>` entry per line. Only `state == device` counts as Attached. For `unauthorized`, print "Allow USB debugging on the device"; ignore `offline`.

**Connect**:

1. Pick a free port P on the computer: call `net.Listen("tcp", "127.0.0.1:0")`, read the assigned port, and close the listener.
2. Open a new connection and send `host-serial:<serial>:forward:tcp:P;tcp:31416`.
3. Read `OKAY` twice: the first acknowledges the request, and the second confirms that forwarding was installed. Refer to `handle_forward_request` in the adb source.
4. `net.Dial("tcp", "127.0.0.1:P")` reaches device port 31416. If the device is not listening, Dial succeeds but immediately receives EOF because adbd closes the stream after its device-side connect fails. Retry every 2 s.
5. When the tunnel ends, send `host-serial:<serial>:killforward:tcp:P`.

Choose the port locally instead of using `tcp:0` to avoid parsing the protocol details of adb's assigned-port response.

---

## 7. iOS client (M1)

### 7.1 Project configuration

| Setting | Value |
|----|----|
| Deployment target | iOS 15.0 |
| Target 1 | `ReverseTether` (container app), bundle `dev.fun.revtether` |
| Target 2 | `ReverseTetherTunnel` (Network Extension, Packet Tunnel), bundle `dev.fun.revtether.tunnel` |
| Entitlements for both targets | `com.apple.developer.networking.networkextension` = `[packet-tunnel-provider]` |
| App Group (optional) | `group.dev.fun.revtether`, used to share statistics with the container app |
| Extension Info.plist | `NSExtension.NSExtensionPointIdentifier = com.apple.networkextension.packet-tunnel`; `NSExtensionPrincipalClass = $(PRODUCT_MODULE_NAME).PacketTunnelProvider` |

### 7.2 Container app

On launch: load or create the configuration → save → start the tunnel. The first `saveToPreferences` call triggers the system prompt to allow adding a VPN configuration.

```swift
func loadOrCreateManager() async throws -> NETunnelProviderManager {
    let existing = try await NETunnelProviderManager.loadAllFromPreferences()
    let m = existing.first ?? NETunnelProviderManager()
    let p = NETunnelProviderProtocol()
    p.providerBundleIdentifier = "dev.fun.revtether.tunnel"
    p.serverAddress = "USB"                 // Must be nonempty; the value itself has no meaning.
    m.protocolConfiguration = p
    m.localizedDescription = "USB Reverse Tethering"
    m.isEnabled = true
    try await m.saveToPreferences()
    try await m.loadFromPreferences()        // Reload after saving, or startup will fail.
    return m
}

// onAppear:
let m = try await loadOrCreateManager()
try m.connection.startVPNTunnel()
// Observe NEVPNStatusDidChange to update the UI; provide a Stop button that calls m.connection.stopVPNTunnel().
```

The UI needs three states: disconnected, enabled and waiting for the computer, and connected. Upload / download rates can be displayed using statistics shared through the App Group or fetched through `sendProviderMessage`.

### 7.3 PacketTunnelProvider

```swift
final class PacketTunnelProvider: NEPacketTunnelProvider {
    private let queue = DispatchQueue(label: "revtether.tunnel")
    private var listener: NWListener?
    private var conn: NWConnection?
    private var rx = Data()                       // Frame decoding buffer
    private var pending = 0                       // Bytes submitted but not yet sent
    private let maxPending = 2 * 1024 * 1024      // Drop beyond this limit to protect extension memory.
    private var keepalive: DispatchSourceTimer?
    private var lastRecv = Date()

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        let s = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "198.18.0.1")
        let v4 = NEIPv4Settings(addresses: ["198.18.0.2"], subnetMasks: ["255.255.255.0"])
        v4.includedRoutes = [NEIPv4Route.default()]
        v4.excludedRoutes = [
            NEIPv4Route(destinationAddress: "10.0.0.0",    subnetMask: "255.0.0.0"),
            NEIPv4Route(destinationAddress: "172.16.0.0",  subnetMask: "255.240.0.0"),
            NEIPv4Route(destinationAddress: "192.168.0.0", subnetMask: "255.255.0.0"),
            NEIPv4Route(destinationAddress: "169.254.0.0", subnetMask: "255.255.0.0"),
        ]
        s.ipv4Settings = v4
        s.dnsSettings = NEDNSSettings(servers: ["198.18.0.1"])
        s.mtu = 1400
        setTunnelNetworkSettings(s) { [self] err in
            if let err { completionHandler(err); return }
            startListener()
            readLoop()
            completionHandler(nil)
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        keepalive?.cancel(); conn?.cancel(); listener?.cancel()
        completionHandler()
    }

    private func startListener() {
        let params = NWParameters.tcp
        params.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: 31416)
        params.allowLocalEndpointReuse = true
        listener = try? NWListener(using: params)
        listener?.newConnectionHandler = { [weak self] c in self?.queue.async { self?.accept(c) } }
        listener?.start(queue: queue)
    }

    private func accept(_ c: NWConnection) {
        guard conn == nil else { c.cancel(); return }      // Allow only one host at a time.
        conn = c; rx.removeAll(); pending = 0; lastRecv = Date()
        c.stateUpdateHandler = { [weak self] st in
            switch st { case .failed, .cancelled: self?.queue.async { self?.dropConn() }; default: break }
        }
        c.start(queue: queue)
        send(Frame.hello(platform: 1))
        startKeepalive()
        receiveLoop()
    }

    private func dropConn() { keepalive?.cancel(); conn = nil }   // Keep the VPN enabled and wait for the next host.

    // tun → host
    private func readLoop() {
        packetFlow.readPackets { [weak self] packets, protos in
            guard let self else { return }
            queue.async {
                if self.conn != nil, self.pending < self.maxPending {
                    var buf = Data()
                    for (i, p) in packets.enumerated() where protos[i].int32Value == AF_INET {
                        Frame.append(&buf, type: .ip, payload: p)
                    }
                    self.send(buf)
                }                                              // Otherwise drop: no host or backpressure.
                self.readLoop()
            }
        }
    }

    private func send(_ d: Data) {
        guard let c = conn, !d.isEmpty else { return }
        pending += d.count
        c.send(content: d, completion: .contentProcessed { [weak self] _ in self?.queue.async { self?.pending -= d.count } })
    }

    // host → tun
    private func receiveLoop() {
        conn?.receive(minimumIncompleteLength: 1, maximumLength: 65536) { [weak self] data, _, done, err in
            guard let self else { return }
            if let data { self.rx.append(data); self.lastRecv = Date(); self.drainFrames() }
            if done || err != nil { self.dropConn(); return }
            self.receiveLoop()
        }
    }

    private func drainFrames() {
        var pkts: [Data] = []
        while let f = Frame.next(from: &rx) {        // Return nil for incomplete frames and retain the remaining bytes.
            switch f.type {
            case .ip: pkts.append(f.payload)
            case .hello, .keepalive: break
            }
        }
        if !pkts.isEmpty {
            packetFlow.writePackets(pkts, withProtocols: Array(repeating: NSNumber(value: AF_INET), count: pkts.count))
        }
    }

    private func startKeepalive() {
        let t = DispatchSource.makeTimerSource(queue: queue)
        t.schedule(deadline: .now() + 5, repeating: 5)
        t.setEventHandler { [weak self] in
            guard let self else { return }
            if Date().timeIntervalSince(self.lastRecv) > 15 { self.conn?.cancel(); return }
            self.send(Frame.keepalive())
        }
        t.resume(); keepalive = t
    }
}
```

Key points:

- Access all state only on `queue`.
- `pending` provides backpressure: slow host reads create a backlog of NWConnection sends. Drop new packets after 2 MB and rely on TCP retransmission to keep extension memory bounded.
- Keep the VPN enabled and drop packets when no host is connected, preventing traffic from falling back to cellular. The user stops it manually in the container app.
- Log with `os_log`; do not write log files.

---

## 8. Android client (M2)

### 8.1 Project configuration

- Kotlin, minSdk 21, targetSdk 34
- `applicationId` / `namespace`: `dev.fun.revtether`
- `AndroidManifest.xml`:
  ```xml
  <uses-permission android:name="android.permission.INTERNET"/>
  <uses-permission android:name="android.permission.FOREGROUND_SERVICE"/>
  <uses-permission android:name="android.permission.FOREGROUND_SERVICE_SYSTEM_EXEMPTED"/>
  <uses-permission android:name="android.permission.POST_NOTIFICATIONS"/>
  <service android:name=".TunnelService"
           android:permission="android.permission.BIND_VPN_SERVICE"
           android:exported="false"
           android:foregroundServiceType="systemExempted">
      <intent-filter><action android:name="android.net.VpnService"/></intent-filter>
  </service>
  ```
  Android 14+ requires a declared foreground service type. VPNs fall under the `systemExempted` category (to be validated in M0).

### 8.2 MainActivity

```kotlin
override fun onCreate(...) {
    val intent = VpnService.prepare(this)
    if (intent != null) startActivityForResult(intent, REQ_VPN) else startTunnel()
}
override fun onActivityResult(req: Int, res: Int, data: Intent?) {
    if (req == REQ_VPN && res == RESULT_OK) startTunnel()
}
private fun startTunnel() = ContextCompat.startForegroundService(this, Intent(this, TunnelService::class.java))
```

Settings page: a list of installed apps with multiple selection for exclusions. Store the selection in SharedPreferences and restart the service for changes to take effect.

### 8.3 TunnelService

```kotlin
class TunnelService : VpnService() {
    @Volatile private var running = false
    private var tun: ParcelFileDescriptor? = null
    @Volatile private var sock: Socket? = null
    private val writeLock = Any()

    override fun onStartCommand(i: Intent?, f: Int, id: Int): Int {
        startForeground(NOTIF_ID, buildNotification("Waiting for computer"))
        val b = Builder().setSession("USB Reverse Tethering").setMtu(1400)
            .addAddress("198.18.0.2", 24).addDnsServer("198.18.0.1").setBlocking(true)
        if (Build.VERSION.SDK_INT >= 33) {
            b.addRoute("0.0.0.0", 0)
            listOf("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16")
                .forEach { b.excludeRoute(IpPrefix(it)) }
        } else {
            ROUTES_EXCLUDING_LAN.forEach { (a, p) -> b.addRoute(a, p) }
        }
        prefs.disallowedApps.forEach { runCatching { b.addDisallowedApplication(it) } }
        tun = b.establish() ?: run { stopSelf(); return START_NOT_STICKY }
        running = true
        thread(name = "tun-reader") { tunReader() }
        thread(name = "server") { serverLoop() }
        return START_STICKY
    }

    private fun serverLoop() {
        ServerSocket(31416, 1, InetAddress.getLoopbackAddress()).use { server ->
            while (running) {
                val s = runCatching { server.accept() }.getOrNull() ?: continue
                s.tcpNoDelay = true
                sock = s
                updateNotification("Connected")
                sendHello(s); runCatching { sockReader(s) }   // Block until disconnected.
                sock = null
                updateNotification("Waiting for computer")
            }
        }
    }

    // tun → host. Keep reading and dropping without a host to prevent traffic leaks and kernel queue buildup.
    private fun tunReader() {
        val input = FileInputStream(tun!!.fileDescriptor)
        val buf = ByteArray(1500)
        while (running) {
            val n = input.read(buf); if (n <= 0) continue
            if (buf[0].toInt() ushr 4 != 4) continue            // Drop non-IPv4 packets.
            val s = sock ?: continue
            runCatching { synchronized(writeLock) { Frame.write(s.getOutputStream(), TYPE_IP, buf, n) } }
        }
    }

    // host → tun
    private fun sockReader(s: Socket) {
        val input = BufferedInputStream(s.getInputStream(), 64 * 1024)
        val out = FileOutputStream(tun!!.fileDescriptor)
        s.soTimeout = 15_000                                     // Disconnect after 15 s without a frame.
        while (running) {
            val (type, payload) = Frame.read(input)
            when (type) { TYPE_IP -> out.write(payload); else -> {} }
        }
    }
    // keepalive: ScheduledExecutor writes TYPE_KEEPALIVE under writeLock every 5 s.
}
```

### 8.4 `ROUTES_EXCLUDING_LAN` (Android < 13)

`0.0.0.0/0` minus `10/8`, `172.16/12`, `192.168/16`, and `169.254/16`, excluding 224/3 so multicast and reserved ranges stay outside the tunnel:

```
0.0.0.0/5      8.0.0.0/7      11.0.0.0/8     12.0.0.0/6     16.0.0.0/4     32.0.0.0/3
64.0.0.0/2     128.0.0.0/3    160.0.0.0/5    168.0.0.0/8    169.0.0.0/9    169.128.0.0/10
169.192.0.0/11 169.224.0.0/12 169.240.0.0/13 169.248.0.0/14 169.252.0.0/15 169.255.0.0/16
170.0.0.0/7    172.0.0.0/12   172.32.0.0/11  172.64.0.0/10  172.128.0.0/9  173.0.0.0/8
174.0.0.0/7    176.0.0.0/4    192.0.0.0/9    192.128.0.0/11 192.160.0.0/13 192.169.0.0/16
192.170.0.0/15 192.172.0.0/14 192.176.0.0/12 192.192.0.0/10 193.0.0.0/8    194.0.0.0/7
196.0.0.0/6    200.0.0.0/5    208.0.0.0/4
```

39 routes in total. The 196.0.0.0/6 route covers 198.18.0.0/24.

---

## 9. CLI

```
revtether run [--device <id>] [--verbose] [--pcap <file>]
revtether devices
revtether install [--device <serial>]      # adb install of the embedded APK
revtether version
```

Main loop for `revtether run`:

```
Start usbmux.Watch and adb.Watch (warn if either is unavailable; do not exit).
for ev := range merged events:
    Attached  → start a deviceLoop(ev) goroutine
    Detached  → cancel the corresponding deviceLoop

deviceLoop:
    while ctx is not canceled:
        stream, err := transport.Connect(ctx, id, 31416)
        if err != nil: display "Open the app on the device", sleep 2s, continue
        err = tunnel.Run(ctx, stream, dialer.Direct, log)   // Block until disconnected.
        display "Disconnected: <reason>", sleep 1s
```

Refresh one terminal line per device every second: `[iOS 00008030-…]  connected  tcp:12 udp:3  ↑1.2MB/s ↓340KB/s`. `--verbose` logs each connection opening and closing.

Build with `GOOS=darwin/linux GOARCH=amd64/arm64 go build`; embed the APK with `//go:embed`.

---

## 10. State machines

Device side, identical on both platforms:

```
Stopped ──app launch──► Listening ──host connects + HELLO──► Connected
Listening ◄──host disconnects / no frames for 15s── Connected
Listening / Connected ──user stops──► Stopped
```

Computer side, per device:

```
Attached ──Connect succeeds──► Handshake ──HELLO OK──► Running ──stream closes──► Reconnecting(2s) ──► Connect…
Attached ──Connect fails──► Reconnecting(2s)
Any state ──Detached──► Destroyed
```

---

## 11. Errors and edge cases

| Situation | Handling |
|------|------|
| USB connected but the app is not running | usbmux Result 3 / immediate adb EOF → retry every 2 s and show a terminal message |
| USB unplugged | Detached → cancel; the device keeps the VPN enabled and waits |
| Protocol version mismatch | The computer closes the connection and prompts the user to upgrade the device app |
| Destination unreachable / connection refused | Return TCP RST; silently drop UDP |
| Computer changes networks | Host socket error → close the device-side connection and let the app reconnect; reread resolv.conf after its 5 s cache expires |
| Many short connections | `maxInFlight = 4096`; drop excess SYNs and wait for retransmission |
| UDP session limit exceeded | Drop new sessions |
| IPv6 packet received | Filtered on the device; the relay also checks the version field and drops anything other than 4 |
| Invalid frame | Disconnect and reconnect |
| A second revtether instance connects to the same device | The device rejects the second connection |
| adb device is `unauthorized` | Prompt the user to allow USB debugging on the device |
| Linux has no `/var/run/usbmuxd` | Prompt the user to install usbmuxd |

---

## 12. Performance

- Targets: TCP throughput ≥ 200 Mbps for one device, added latency < 2 ms, and relay resident memory < 50 MB per device.
- Device-side TUN I/O and copying over a single stream are the expected bottlenecks; netstack and Go scheduling are not expected to be the bottleneck.
- Validation: run `iperf3 -c <computer-IP>` on the device against the computer's iperf3 server through the tunnel; use `ping 198.18.0.1`, `nslookup`, and the Speedtest app.

---

## 13. Testing

- **Unit tests**: framing boundary lengths and truncation; usbmux plist encoding / decoding; adb response parsing; the DNS TC-bit branch; UDP session eviction.
- **Stack integration without devices**: inject handcrafted SYN / UDP packets through `channel.Endpoint`; check the Dialer destination, RST behavior, and half-close propagation.
- **End-to-end**: physical iOS device + macOS; Android emulator (suitable for CI) + physical Android device.
- **Failure scenarios**: unplug and reconnect 10 times, kill the app, switch the computer's network, and resume after locking the device for 30 minutes.
- **Memory**: iOS extension memory stays below 30 MB after 5 minutes of continuous downloading, measured with Xcode Memory Report.

---

## 14. Milestones

| Phase | Scope | Acceptance |
|------|------|------|
| **M0: Technical validation** | Five spikes from Section 15, each with a minimal demo | Confirm all assumptions or adjust the design |
| **M1: Core + iOS** | framing, tunnel, usbmux transport, CLI skeleton; iOS container app + extension | A physical iOS device opens web pages through the tunnel, meets the Speedtest target, and pings successfully |
| **M2: Android** | adb transport, Android app, `revtether install` | The same acceptance criteria on a physical Android device |
| **M3: Complete features** | DNS TC / TCP fallback, app exclusions, LAN bypass, multiple devices, terminal status, `--pcap` | Two devices work at once; LAN devices remain reachable; excluded apps use cellular |
| **M4: Release** | Homebrew formula, GitHub Release, TestFlight, Linux documentation (usbmuxd + idevicepair) | A new user can follow the README and get connected within 10 minutes |
| v2 | IPv6, upstream proxy Dialer, Windows, macOS split-DNS | — |

---

## 15. M0 technical validation checklist

The following assumptions require confirmation on physical devices. Any failure affects the design:

1. **usbmux Connect can reach an NWListener bound to `127.0.0.1:31416` inside the extension.** If the device's usbmux proxy connects from a non-loopback address, bind to `0.0.0.0` and verify that the accepted peer belongs to the device.
2. **Whether an unpaired iOS device that has not trusted the computer can Connect to an ordinary port.** If not, require `idevicepair pair` in the Linux documentation and use the system trust prompt on macOS.
3. **The iOS extension's memory profile with `pending` backpressure:** stay below 30 MB during continuous downloading.
4. **Android 14+ permits `foregroundServiceType="systemExempted"` for VpnService.** Otherwise, use `specialUse` and provide `PROPERTY_SPECIAL_USE_FGS_SUBTYPE`.
5. **The two OKAY responses to adb `forward` and immediate EOF when the device is not listening.** Confirm behavior against the actual adb version (≥ 1.0.41).

---

## 16. Risks

- The iOS extension's memory limit is about 50 MB. Bounded send queues and packet dropping address this; measure it in M0.
- macOS split-DNS is not supported, so some corporate intranet names may fail to resolve. Address this in v2.
- The 39 routes on Android < 13 need device testing to confirm that `addRoute` accepts this many entries. Gnirehtet-style projects have demonstrated that this is feasible.
- gVisor is a substantial dependency: the binary is about 20 MB and builds take longer. This is considered acceptable.
- iOS cannot exclude traffic by app. Only LAN bypass is available; explain this in the product documentation.
