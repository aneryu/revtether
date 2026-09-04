# Reverse Tether — 跨平台 USB 反向共享网络技术方案 v1.1

支持 Android 与 iOS 的类 Gnirehtet 工具，命令 `revtether`。设备通过 USB 借用电脑网络。

本版修订要点（相对 v1.0）：
- iOS 提前到 M1，Android 顺延到 M2
- 修正隧道网段与"局域网直连"排除段冲突的问题（10.0.0.0/24 改为 198.18.0.0/24）
- 补全 usbmux / adb 线上协议细节、gVisor 接入代码、iOS / Android 工程配置与代码骨架
- 增加 M0 技术验证清单，标出所有需要真机确认的假设
- 增加 `--pcap` 抓帧调试、单写者出站队列等落地细节

---

## 1. 目标与非目标

**目标**
- 用户在设备上打开 App（App 启动即开启 VPN 并监听），在电脑上运行 `revtether run`，设备即通过 USB 使用电脑网络
- 设备端全量 IPv4 流量（TCP / UDP / ICMP echo）可达互联网
- macOS / Linux 桌面端，单二进制，无运行时依赖（Linux 需系统 `usbmuxd`）
- 多台设备（混合平台）同时接入

**非目标（v1）**
- Windows；IPv6；Wi-Fi / adb 无线调试；上游代理链（仅预留接口）；电脑侧自动拉起设备 App；设备无需安装 App

---

## 2. 决策记录

| # | 决策 | 结论 |
|---|------|------|
| D1 | Relay 技术栈 | Go 1.22+，gVisor netstack（`gvisor.dev/gvisor@go` 分支） |
| D2 | 连接方向 | 电脑主动连接设备：Android 走 `adb forward`，iOS 走 usbmuxd `Connect` |
| D3 | usbmuxd 接入 | 自实现 usbmux plist 协议 |
| D4 | 桌面平台 | macOS（arm64 / amd64）+ Linux（amd64 / arm64） |
| D5 | IPv6 | 不支持，不下发 v6 路由 |
| D6 | DNS | relay 拦截 UDP 53，原始报文转发给电脑系统 nameserver |
| D7 | 流复用 | 单流 + TCP 窗口背压 |
| D8 | UDP / ICMP | UDP 60 s 空闲回收、上限 1024 会话；ICMP 仅回应 echo |
| D9 | 应用范围 | Android 支持排除 App；两端局域网网段（RFC1918 + 169.254/16）不进隧道 |
| D10 | 启动方式 | 用户手动打开 App，App 启动即开启 VPN |
| D11 | 传输 | 仅 USB |
| D12 | 多设备 | 支持，每设备独立 goroutine 组 + 独立 netstack |
| D13 | 上游出口 | 预留 `Dialer` 接口，v1 直连 |
| D14 | MTU | 1400 |
| D15 | 隧道网段 | `198.18.0.0/24`（RFC 2544 基准测试段，不与任何局域网段冲突） |

---

## 3. 总体架构

```
┌──────────────────── 设备 ────────────────────┐        ┌────────────── 电脑 revtether ──────────────┐
│ iOS: NEPacketTunnelProvider                  │  USB   │ transport/usbmux  Listen/Connect(31416)    │
│   packetFlow ─► 帧 ─► NWListener 127.0.0.1:31416 ◄────►│ transport/adb     track-devices/forward    │
│ Android: VpnService                          │        │                                            │
│   tun fd ─► 帧 ─► ServerSocket 127.0.0.1:31416        │ tunnel ×N ─┬─ framing                       │
└──────────────────────────────────────────────┘        │            ├─ channel.Endpoint ⇄ netstack   │
                                                         │            ├─ tcp.Forwarder → Dialer         │
                                                         │            ├─ udp.Forwarder → Dialer / dns   │
                                                         │            └─ icmp echo（进栈前拦截）         │
                                                         └────────────────────────────────────────────┘
```

设备端只做两件事：把系统流量变成裸 IP 包、在 loopback 上监听 31416。电脑端把"连到设备的 31416"抽象成一条字节流，之后两平台处理完全一致。

### 3.1 地址规划

| 用途 | 地址 |
|------|------|
| 隧道网段 | 198.18.0.0/24 |
| 设备地址 | 198.18.0.2 |
| 网关 / DNS | 198.18.0.1 |

选 198.18/24 的原因：局域网直连需要把 10/8、172.16/12、192.168/16、169.254/16 排除出隧道，隧道自身地址不能落在这些段内，否则到网关/DNS 的流量会被排除规则截走。每设备一个独立 netstack，地址可重复。

---

## 4. 线协议

设备与电脑之间是一条可靠字节流，承载帧序列。所有多字节整数大端。

```
+------+--------+-----------------+
| type | length | payload         |
| u8   | u16    | length bytes    |
+------+--------+-----------------+
```

| type | 名称 | payload | 方向 |
|------|------|---------|------|
| 0x00 | HELLO | `version u8`(=1) `platform u8`(0=android 1=ios 0xFF=host) `caps u16`(保留 0) | 双向 |
| 0x01 | IP | 原始 IPv4 包，1 ≤ length ≤ 1500 | 双向 |
| 0x02 | KEEPALIVE | 空 | 双向 |

规则：
- 电脑连上后**设备先发 HELLO**，电脑校验 version 后回 HELLO；不匹配则电脑关闭连接并在终端提示升级
- 双方每 5 s 发 KEEPALIVE；连续 15 s 未收到任何帧视为断开
- length 超过 1500、type 未知、HELLO 之前收到其他帧 → 协议错误，断开
- 不加密不压缩

---

## 5. Relay Core（Go）

### 5.1 代码结构

```
cmd/revtether/main.go       CLI
internal/
  framing/framing.go        帧编解码
  transport/
    transport.go            Transport 接口、DeviceEvent
    usbmux/usbmux.go        usbmuxd 客户端
    adb/adb.go              adb server 客户端
  tunnel/
    tunnel.go               Run()：握手、建栈、goroutine 编排、生命周期
    stack.go                newStack()
    tcp.go                  TCP forwarder handler
    udp.go                  UDP forwarder handler + 会话计数
    dns.go                  UDP 53 转发
    icmp.go                 echo reply
    stats.go                计数器
  dialer/dialer.go          Dialer 接口 + 直连实现
  resolvconf/resolvconf.go  读取系统 nameserver（5 s 缓存）
  ui/status.go              终端状态刷新
```

依赖：`gvisor.dev/gvisor`（go 分支）、`howett.net/plist`。其余标准库。

### 5.2 帧编解码

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

// Read 返回的 payload 是新分配的切片，调用方可持有。
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

### 5.3 netstack 初始化

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
    s.SetPromiscuousMode(nicID, true) // 接受任意目标地址
    s.SetSpoofing(nicID, true)        // 允许以任意源地址回包
    s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nicID}})
    return s, nil
}
```

LinkEndpoint 直接使用 `pkg/tcpip/link/channel`：`channel.New(256, 1400, "")`。它提供 `InjectInbound`（帧 → 栈）和 `ReadContext`（栈 → 帧），wireguard-go 的 netstack 模式即如此接法。

> gVisor 的 API 名称随版本略有出入（例如 `gonet.NewUDPConn` 旧版多一个 `stack` 参数），以实际引入的 go 分支 commit 为准。参考实现：`xjasonlyu/tun2socks` 的 `core/` 目录。

### 5.4 Tunnel 生命周期与并发模型

```go
func Run(ctx context.Context, stream io.ReadWriteCloser, d dialer.Dialer, log *slog.Logger) error
```

1. **握手**：读第一帧必须是 HELLO，校验 version，回 HELLO
2. **建栈**：`ep := channel.New(...)`，`s := newStack(ep)`，注册 TCP/UDP forwarder
3. **goroutines**（`errgroup`，任一退出即全部取消）：
   - `inbound`：`stream → 解帧 → ICMP 拦截 / InjectInbound`
   - `outbound`：`ep.ReadContext → out chan`
   - `writer`：**唯一写者**，从 `out chan []byte`（容量 512）与 keepalive ticker 取数据，用 `bufio.Writer` 批量写 stream；channel 满时丢弃（`select default`），依赖 TCP 重传
   - `watchdog`：15 s 未收帧则取消 ctx
4. **退出**：取消 ctx → 关闭 stream → `s.Close()`；等待所有 goroutine 结束后 `s.Destroy()`

inbound 核心：

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

outbound 核心：

```go
for {
    pkt := ep.ReadContext(ctx)
    if pkt == nil { return nil }
    v := pkt.ToView()
    enqueue(append([]byte(nil), v.AsSlice()...))
    v.Release(); pkt.DecRef()
}
```

### 5.5 TCP 转发

```go
fwd := tcp.NewForwarder(s, 256<<10 /*rcvWnd*/, 4096 /*maxInFlight*/, func(r *tcp.ForwarderRequest) {
    id := r.ID()
    target := net.JoinHostPort(net.IP(id.LocalAddress.AsSlice()).String(), strconv.Itoa(int(id.LocalPort)))
    dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
    hostConn, err := d.DialContext(dctx, "tcp", target)
    cancel()
    if err != nil { r.Complete(true); return } // RST：设备侧真实感知目标不可达
    var wq waiter.Queue
    ep, terr := r.CreateEndpoint(&wq)
    if terr != nil { hostConn.Close(); r.Complete(true); return }
    r.Complete(false)
    devConn := gonet.NewTCPConn(&wq, ep)
    go relayTCP(devConn, hostConn)
})
s.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)
```

`relayTCP`：两个 `io.Copy`，一方向 EOF 时对另一端 `CloseWrite()`（半关闭），两方向都结束后 Close。缓冲区 32 KB。不设空闲超时。

先 Dial 再 `CreateEndpoint`，SYN-ACK 延后；设备侧 SYN 重传（1 s / 2 s / 4 s）期间 forwarder 会识别为同一个 in-flight 请求，不会重复 Dial。

### 5.6 UDP 转发

```go
ufwd := udp.NewForwarder(s, func(r *udp.ForwarderRequest) {
    if !sessions.TryAcquire() { return } // 超过 1024 会话直接丢
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

`relayUDP`：两个读循环，每次读写刷新 `lastActive`；独立 timer 每 10 s 检查，空闲超过 60 s 关闭双端并 `Release()`。

### 5.7 DNS 转发

设备 DNS 指向 198.18.0.1，所有查询必经隧道且目标端口 53，在 UDP forwarder 内分流：

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
    for _, ns := range resolvconf.Nameservers() {          // 5 s 缓存；为空时回退 1.1.1.1, 8.8.8.8
        resp, err := exchangeUDP(ns, q, 3*time.Second)
        if err != nil { continue }
        if resp[2]&0x02 != 0 {                              // TC 位：改走 TCP 重查
            if r2, err := exchangeTCP(ns, q, 3*time.Second); err == nil { return r2, nil }
        }
        return resp, nil
    }
    return nil, ErrNoNameserver
}
```

不解析 DNS 语义，任意记录类型 / EDNS 天然透传。`resolvconf` 解析 `/etc/resolv.conf` 的 `nameserver` 行（macOS 主 resolver 也写在这里；Linux systemd-resolved 的 `127.0.0.53` 同样可用，因为 relay 就在电脑上）。macOS 按域名的 split-DNS 不生效，列入 v2。

### 5.8 ICMP echo

在 `InjectInbound` 前拦截：IPv4 且 protocol=1 且 ICMP type=8 → 构造 reply：交换 src/dst，type=0，TTL=64，重算 ICMP 校验和（`header.ICMPv4Checksum`）与 IP 头校验和，通过 `enqueue` 直接写回。其余 ICMP 丢弃。效果是设备 ping 任何地址都通，仅用于验证隧道存活。

### 5.9 Dialer 接口

```go
type Dialer interface {
    DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}
var Direct Dialer = &net.Dialer{Timeout: 10 * time.Second}
```

v2 可实现 SOCKS5 / HTTP CONNECT 版本替换，隧道层零改动。

### 5.10 调试：`--pcap <file>`

writer / inbound 各自把经手的 IP 包以 pcap 格式（linktype RAW=101）追加写入文件，Wireshark 直接打开。实现 ~40 行，是定位协议栈问题最有效的手段。

---

## 6. 传输层

### 6.1 接口

```go
type Platform uint8
const ( Android Platform = 0; IOS Platform = 1 )

type DeviceEvent struct {
    ID       string   // usbmux DeviceID(字符串化) 或 adb serial
    Serial   string   // UDID / serial，用于显示
    Platform Platform
    Attached bool
}

type Transport interface {
    Watch(ctx context.Context) (<-chan DeviceEvent, error)
    Connect(ctx context.Context, deviceID string, port uint16) (io.ReadWriteCloser, error)
}
```

### 6.2 iOS：usbmuxd 协议

**连接**：unix socket `/var/run/usbmuxd`。macOS 系统自带；Linux 安装 `usbmuxd` 包（Debian/Ubuntu：`apt install usbmuxd`），socket 默认 0666。

**帧格式**（头部 16 字节，全部小端）：

```
u32 length     // 含头部的总长度
u32 version    // = 1（plist 协议）
u32 msgType    // = 8（plist）
u32 tag        // 请求序号，响应原样带回
[XML plist]
```

**Listen**（一条长连接，用于 Watch）：

```xml
<dict>
  <key>MessageType</key><string>Listen</string>
  <key>ClientVersionString</key><string>revtether</string>
  <key>ProgName</key><string>revtether</string>
  <key>kLibUSBMuxVersion</key><integer>3</integer>
</dict>
```

响应 `{MessageType: Result, Number: 0}`，之后持续推送：
- `{MessageType: Attached, DeviceID: <int>, Properties: {ConnectionType: "USB", SerialNumber: "<UDID>", ProductID, LocationID, ...}}`
- `{MessageType: Detached, DeviceID: <int>}`

只处理 `ConnectionType == "USB"` 的设备（Wi-Fi 配对设备会以 `Network` 出现，忽略）。

**Connect**（每次 Connect 新开一条连接）：

```xml
<dict>
  <key>MessageType</key><string>Connect</string>
  <key>DeviceID</key><integer>5</integer>
  <key>PortNumber</key><integer>47226</integer>   <!-- 31416 字节交换后的值：htons(31416) = 0xB87A = 47226 -->
  <key>ClientVersionString</key><string>revtether</string>
  <key>ProgName</key><string>revtether</string>
</dict>
```

响应 `{MessageType: Result, Number: N}`：
- `0`：成功，**此后该 socket 就是到设备 31416 的裸字节流**，直接交给 tunnel
- `3`：连接被拒（设备上没人监听 → App 未启动），每 2 s 重试
- `2`：设备不存在（刚拔出），放弃
- 其他：记录并按 2 s 重试

**ListDevices**（`revtether devices` 用）：`{MessageType: ListDevices}` → `{DeviceList: [...]}`。

**配对**：设备需已信任电脑。macOS 插入设备时系统自动弹出"信任"流程；Linux 需先用 `idevicepair pair`（libimobiledevice-utils）完成一次。是否未配对也能 Connect 到普通端口，列入 M0 验证。

### 6.3 Android：adb server 协议

**连接**：TCP `127.0.0.1:5037`。不可达时执行一次 `adb start-server`（要求 PATH 有 adb），仍失败则提示。

**请求格式**：4 位十六进制长度 + 请求字符串，例如 `001ahost:track-devices`。响应 `OKAY` 或 `FAIL` + 4 位 hex 长度 + 错误文本。

**Watch**：`host:track-devices` 长连接。每次变化推送一段 `4 hex 长度` + 文本，每行 `<serial>\t<state>`。只有 `state == device` 视为 Attached；`unauthorized` 在终端提示"请在设备上允许 USB 调试"；`offline` 忽略。

**Connect**：
1. 在电脑选一个空闲端口 P：`net.Listen("tcp", "127.0.0.1:0")` 取端口后关闭
2. 新连接发 `host-serial:<serial>:forward:tcp:P;tcp:31416`
3. 读两次 `OKAY`（第一次为请求受理，第二次为 forward 安装成功；以 adb 源码 `handle_forward_request` 为准）
4. `net.Dial("tcp", "127.0.0.1:P")` 即到达设备 31416；设备端未监听时 Dial 成功但会立刻收到 EOF（adbd 在设备端 connect 失败后关闭），同样按 2 s 重试
5. 隧道结束：`host-serial:<serial>:killforward:tcp:P`

用自选端口而非 `tcp:0`，避免解析 adb 返回端口的协议细节。

---

## 7. iOS 客户端（M1）

### 7.1 工程配置

| 项 | 值 |
|----|----|
| 部署目标 | iOS 15.0 |
| Target 1 | `ReverseTether`（容器 App），bundle `dev.fun.revtether` |
| Target 2 | `ReverseTetherTunnel`（Network Extension，Packet Tunnel），bundle `dev.fun.revtether.tunnel` |
| 两个 target 的 entitlements | `com.apple.developer.networking.networkextension` = `[packet-tunnel-provider]` |
| App Group（可选） | `group.dev.fun.revtether`，用于向容器 App 共享统计 |
| Extension Info.plist | `NSExtension.NSExtensionPointIdentifier = com.apple.networkextension.packet-tunnel`；`NSExtensionPrincipalClass = $(PRODUCT_MODULE_NAME).PacketTunnelProvider` |

### 7.2 容器 App

启动即：加载/创建配置 → 保存 → 启动隧道。首次 `saveToPreferences` 会触发系统"允许添加 VPN 配置"弹窗。

```swift
func loadOrCreateManager() async throws -> NETunnelProviderManager {
    let existing = try await NETunnelProviderManager.loadAllFromPreferences()
    let m = existing.first ?? NETunnelProviderManager()
    let p = NETunnelProviderProtocol()
    p.providerBundleIdentifier = "dev.fun.revtether.tunnel"
    p.serverAddress = "USB"                 // 必须非空，内容无意义
    m.protocolConfiguration = p
    m.localizedDescription = "USB Reverse Tethering"
    m.isEnabled = true
    try await m.saveToPreferences()
    try await m.loadFromPreferences()        // 保存后必须重新 load，否则 start 报错
    return m
}

// onAppear:
let m = try await loadOrCreateManager()
try m.connection.startVPNTunnel()
// 监听 NEVPNStatusDidChange 更新 UI；提供"停止"按钮调用 m.connection.stopVPNTunnel()
```

UI 只需三态：未连接 / 已开启等待电脑 / 已连接（可显示上下行速率，通过 App Group 共享或 `sendProviderMessage` 拉取）。

### 7.3 PacketTunnelProvider

```swift
final class PacketTunnelProvider: NEPacketTunnelProvider {
    private let queue = DispatchQueue(label: "revtether.tunnel")
    private var listener: NWListener?
    private var conn: NWConnection?
    private var rx = Data()                       // 解帧缓冲
    private var pending = 0                       // 已提交未完成发送的字节数
    private let maxPending = 2 * 1024 * 1024      // 超过即丢包，保护 extension 内存
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
        guard conn == nil else { c.cancel(); return }      // 同时只允许一个 host
        conn = c; rx.removeAll(); pending = 0; lastRecv = Date()
        c.stateUpdateHandler = { [weak self] st in
            switch st { case .failed, .cancelled: self?.queue.async { self?.dropConn() }; default: break }
        }
        c.start(queue: queue)
        send(Frame.hello(platform: 1))
        startKeepalive()
        receiveLoop()
    }

    private func dropConn() { keepalive?.cancel(); conn = nil }   // 保持 VPN 开启，等待下一个 host

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
                }                                              // 否则丢弃（无 host 或背压）
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
        while let f = Frame.next(from: &rx) {        // 不完整则返回 nil 并保留残余
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

要点：
- 所有状态只在 `queue` 上访问
- `pending` 是背压：host 侧读得慢 → NWConnection 发送积压 → 超过 2 MB 后新包直接丢，由 TCP 重传兜底，extension 内存不会涨
- 无 host 时保持 VPN 开启并丢包，避免流量泄漏到蜂窝；用户在容器 App 手动停止
- 日志用 `os_log`，不写文件

---

## 8. Android 客户端（M2）

### 8.1 工程配置

- Kotlin，minSdk 21，targetSdk 34
- `applicationId` / `namespace`：`dev.fun.revtether`
- `AndroidManifest.xml`：
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
  Android 14+ 前台服务需声明类型，VPN 属于 `systemExempted` 豁免类别（M0 验证）。

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

设置页：排除 App 列表（多选已安装应用），保存在 SharedPreferences，修改后重启服务生效。

### 8.3 TunnelService

```kotlin
class TunnelService : VpnService() {
    @Volatile private var running = false
    private var tun: ParcelFileDescriptor? = null
    @Volatile private var sock: Socket? = null
    private val writeLock = Any()

    override fun onStartCommand(i: Intent?, f: Int, id: Int): Int {
        startForeground(NOTIF_ID, buildNotification("等待电脑连接"))
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
                updateNotification("已连接")
                sendHello(s); runCatching { sockReader(s) }   // 阻塞直到断开
                sock = null
                updateNotification("等待电脑连接")
            }
        }
    }

    // tun → host。无 host 时也持续读并丢弃，避免流量泄漏与内核队列堆积
    private fun tunReader() {
        val input = FileInputStream(tun!!.fileDescriptor)
        val buf = ByteArray(1500)
        while (running) {
            val n = input.read(buf); if (n <= 0) continue
            if (buf[0].toInt() ushr 4 != 4) continue            // 非 IPv4 丢弃
            val s = sock ?: continue
            runCatching { synchronized(writeLock) { Frame.write(s.getOutputStream(), TYPE_IP, buf, n) } }
        }
    }

    // host → tun
    private fun sockReader(s: Socket) {
        val input = BufferedInputStream(s.getInputStream(), 64 * 1024)
        val out = FileOutputStream(tun!!.fileDescriptor)
        s.soTimeout = 15_000                                     // 15 s 无帧视为断开
        while (running) {
            val (type, payload) = Frame.read(input)
            when (type) { TYPE_IP -> out.write(payload); else -> {} }
        }
    }
    // keepalive：ScheduledExecutor 每 5 s 在 writeLock 下写 TYPE_KEEPALIVE
}
```

### 8.4 `ROUTES_EXCLUDING_LAN`（Android < 13）

`0.0.0.0/0` 减去 `10/8`、`172.16/12`、`192.168/16`、`169.254/16`，不含 224/3（组播与保留段不进隧道）：

```
0.0.0.0/5      8.0.0.0/7      11.0.0.0/8     12.0.0.0/6     16.0.0.0/4     32.0.0.0/3
64.0.0.0/2     128.0.0.0/3    160.0.0.0/5    168.0.0.0/8    169.0.0.0/9    169.128.0.0/10
169.192.0.0/11 169.224.0.0/12 169.240.0.0/13 169.248.0.0/14 169.252.0.0/15 169.255.0.0/16
170.0.0.0/7    172.0.0.0/12   172.32.0.0/11  172.64.0.0/10  172.128.0.0/9  173.0.0.0/8
174.0.0.0/7    176.0.0.0/4    192.0.0.0/9    192.128.0.0/11 192.160.0.0/13 192.169.0.0/16
192.170.0.0/15 192.172.0.0/14 192.176.0.0/12 192.192.0.0/10 193.0.0.0/8    194.0.0.0/7
196.0.0.0/6    200.0.0.0/5    208.0.0.0/4
```

共 39 条，198.18.0.0/24 被 196.0.0.0/6 覆盖。

---

## 9. CLI

```
revtether run [--device <id>] [--verbose] [--pcap <file>]
revtether devices
revtether install [--device <serial>]      # adb install 内嵌 APK
revtether version
```

`revtether run` 主循环：

```
启动 usbmux.Watch 与 adb.Watch（任一不可用只告警，不退出）
for ev := range merged events:
    Attached  → 启动 goroutine deviceLoop(ev)
    Detached  → cancel 对应 deviceLoop

deviceLoop:
    for ctx 未取消:
        stream, err := transport.Connect(ctx, id, 31416)
        if err != nil: 显示"等待设备上打开 App"，sleep 2s，continue
        err = tunnel.Run(ctx, stream, dialer.Direct, log)   // 阻塞直到断开
        显示"连接断开: <原因>"，sleep 1s
```

终端每秒刷新一行/设备：`[iOS 00008030-…]  connected  tcp:12 udp:3  ↑1.2MB/s ↓340KB/s`。`--verbose` 输出每条连接的建立/关闭。

构建：`GOOS=darwin/linux GOARCH=amd64/arm64 go build`，APK 通过 `//go:embed` 内嵌。

---

## 10. 状态机

设备端（两平台一致）：

```
Stopped ──App 启动──► Listening ──host 连入 + HELLO──► Connected
Listening ◄──host 断开 / 15s 无帧── Connected
Listening / Connected ──用户停止──► Stopped
```

电脑端每设备：

```
Attached ──Connect 成功──► Handshake ──HELLO OK──► Running ──流断开──► Reconnecting(2s) ──► Connect…
Attached ──Connect 失败──► Reconnecting(2s)
任意状态 ──Detached──► 销毁
```

---

## 11. 错误与边界情况

| 情况 | 处理 |
|------|------|
| USB 已连但 App 未启动 | usbmux Result 3 / adb 立即 EOF → 每 2 s 重试，终端提示 |
| USB 拔出 | Detached → cancel；设备端保持 VPN 等待 |
| 协议版本不匹配 | 电脑关闭连接，终端提示升级设备 App |
| 目标不可达 / 拒绝 | TCP 回 RST；UDP 静默丢弃 |
| 电脑网络切换 | host socket 报错 → 设备侧连接被关闭，应用层重连；resolv.conf 缓存 5 s 后重读 |
| 大量短连接 | `maxInFlight = 4096`，超出的 SYN 丢弃等待重传 |
| UDP 会话超上限 | 新会话丢弃 |
| 收到 IPv6 包 | 设备端已过滤；relay 再校验版本字段，非 4 丢弃 |
| 帧非法 | 断开重连 |
| 第二个 revtether 实例连同一设备 | 设备端拒绝第二个连接 |
| adb 设备 `unauthorized` | 提示在设备上允许 USB 调试 |
| Linux 无 `/var/run/usbmuxd` | 提示安装 usbmuxd |

---

## 12. 性能

- 目标：单设备 TCP 吞吐 ≥ 200 Mbps，附加延迟 < 2 ms，relay 常驻内存 < 50 MB/设备
- 瓶颈在设备端 tun 读写与单流拷贝；netstack 与 Go 调度不是瓶颈
- 验证：设备端 `iperf3 -c <电脑IP>`（电脑 iperf3 server 走隧道）；`ping 198.18.0.1`；`nslookup`；Speedtest App

---

## 13. 测试

- **单元**：framing（边界长度、截断）；usbmux plist 编解码；adb 响应解析；DNS TC 位分支；UDP 会话淘汰
- **协议栈集成（无真机）**：用 `channel.Endpoint` 注入手工构造的 SYN / UDP 包，断言 Dialer 被以正确目标调用、RST 行为、半关闭传播
- **端到端**：iOS 真机 + macOS；Android 模拟器（CI 可跑）+ 真机
- **异常**：反复插拔 10 次、杀 App、电脑切网、设备锁屏 30 分钟后恢复
- **内存**：iOS extension 在持续下载 5 分钟后内存 < 30 MB（Xcode Memory Report）

---

## 14. 里程碑

| 阶段 | 内容 | 验收 |
|------|------|------|
| **M0 技术验证** | 见第 15 节的 5 个 spike，各自一个最小 demo | 全部假设确认或调整方案 |
| **M1 Core + iOS** | framing、tunnel、usbmux transport、CLI 骨架；iOS 容器 App + extension | iOS 真机通过隧道打开网页、Speedtest 达标、ping 通 |
| **M2 Android** | adb transport、Android App、`revtether install` | Android 真机同上验收 |
| **M3 完整功能** | DNS TC/TCP 回退、排除 App、局域网直连、多设备、终端状态、`--pcap` | 两台设备同时使用；局域网设备可达；排除的 App 走蜂窝 |
| **M4 发布** | Homebrew formula、GitHub Release、TestFlight、Linux 文档（usbmuxd + idevicepair） | 新用户按 README 10 分钟内跑通 |
| v2 | IPv6、上游代理 Dialer、Windows、macOS split-DNS | — |

---

## 15. M0 技术验证清单

以下假设需在真机上确认，任一不成立都会影响设计：

1. **usbmux Connect 能到达 extension 内绑定 `127.0.0.1:31416` 的 NWListener**。若设备侧 usbmux 代理从非 loopback 地址发起，改为绑定 `0.0.0.0` 并在 accept 时校验对端为本机
2. **未配对（未"信任"）的 iOS 设备能否 Connect 普通端口**。若不能，Linux 文档要求先 `idevicepair pair`；macOS 依赖系统信任弹窗
3. **iOS extension 在 `pending` 背压下的内存曲线**：持续下载时保持 < 30 MB
4. **Android 14+ `foregroundServiceType="systemExempted"` 对 VpnService 可用**。若不可用改为 `specialUse` 并填写 `PROPERTY_SPECIAL_USE_FGS_SUBTYPE`
5. **adb `forward` 的双 OKAY 响应**与设备端未监听时的 EOF 行为，按实际 adb 版本（≥ 1.0.41）确认

---

## 16. 风险

- iOS extension 内存上限（约 50 MB）：已用有界发送队列 + 丢包控制，M0 实测
- macOS split-DNS 不生效：企业内网部分域名解析失败，v2 处理
- Android < 13 的 39 条路由：需实测 `addRoute` 数量无上限问题（Gnirehtet 类项目已验证可行）
- gVisor 依赖较重：二进制约 20 MB，编译时间较长，可接受
- iOS 无法按 App 排除流量：仅局域网直连，产品层说明
