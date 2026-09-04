import NetworkExtension
import os.log

final class PacketTunnelProvider: NEPacketTunnelProvider {
    private let queue = DispatchQueue(label: "revtether.tunnel")
    private var down: [Data] = []
    private var downBytes = 0
    private let maxDown = 2 * 1024 * 1024
    private var hostSeen = false
    private let log = OSLog(subsystem: "dev.fun.revtether.tunnel", category: "tunnel")

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        // 127.0.0.1 tells NESM this is a local tunnel. lockdownd talks to the
        // container app on :31416; this extension only moves IP packets.
        let s = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "127.0.0.1")
        let v4 = NEIPv4Settings(addresses: ["198.18.0.2"], subnetMasks: ["255.255.255.0"])
        v4.includedRoutes = Self.routesExcludingLoopbackAndLAN
        v4.excludedRoutes = [
            NEIPv4Route(destinationAddress: "127.0.0.0", subnetMask: "255.0.0.0"),
            NEIPv4Route(destinationAddress: "10.0.0.0", subnetMask: "255.0.0.0"),
            NEIPv4Route(destinationAddress: "172.16.0.0", subnetMask: "255.240.0.0"),
            NEIPv4Route(destinationAddress: "192.168.0.0", subnetMask: "255.255.0.0"),
            NEIPv4Route(destinationAddress: "169.254.0.0", subnetMask: "255.255.0.0"),
        ]
        s.ipv4Settings = v4
        s.dnsSettings = NEDNSSettings(servers: ["198.18.0.1"])
        s.mtu = 1400
        setTunnelNetworkSettings(s) { [self] err in
            if let err {
                completionHandler(err)
                return
            }
            self.readLoop()
            completionHandler(nil)
        }
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        queue.async { [self] in
            if hostSeen {
                AppGroup.postHost(connected: false)
            }
            hostSeen = false
            down.removeAll()
            downBytes = 0
            completionHandler()
        }
    }

    override func handleAppMessage(_ messageData: Data, completionHandler: ((Data?) -> Void)?) {
        queue.async { [self] in
            if !hostSeen {
                hostSeen = true
                AppGroup.postHost(connected: true)
                os_log("app relay attached", log: log, type: .info)
            }
            let incoming = TunnelIPC.decode(messageData)
            if !incoming.isEmpty {
                packetFlow.writePackets(
                    incoming,
                    withProtocols: Array(repeating: NSNumber(value: AF_INET), count: incoming.count)
                )
            }
            let outgoing = down
            down.removeAll()
            downBytes = 0
            completionHandler?(TunnelIPC.encode(outgoing))
        }
    }

    private func readLoop() {
        packetFlow.readPackets { [weak self] packets, protos in
            guard let self else { return }
            self.queue.async {
                for (i, p) in packets.enumerated() where protos[i].int32Value == AF_INET {
                    if self.downBytes + p.count > self.maxDown {
                        continue
                    }
                    self.down.append(p)
                    self.downBytes += p.count
                }
                self.readLoop()
            }
        }
    }

    /// 0.0.0.0/0 minus RFC1918, 169.254/16, and 127/8 so lockdownd → localhost
    /// stays on the real stack. 198.18.0.0/24 is covered by 196.0.0.0/6.
    private static let routesExcludingLoopbackAndLAN: [NEIPv4Route] = [
        ("0.0.0.0", "248.0.0.0"),
        ("8.0.0.0", "254.0.0.0"),
        ("11.0.0.0", "255.0.0.0"),
        ("12.0.0.0", "252.0.0.0"),
        ("16.0.0.0", "240.0.0.0"),
        ("32.0.0.0", "224.0.0.0"),
        ("64.0.0.0", "224.0.0.0"),
        ("96.0.0.0", "240.0.0.0"),
        ("112.0.0.0", "248.0.0.0"),
        ("120.0.0.0", "252.0.0.0"),
        ("124.0.0.0", "254.0.0.0"),
        ("126.0.0.0", "255.0.0.0"),
        ("128.0.0.0", "224.0.0.0"),
        ("160.0.0.0", "248.0.0.0"),
        ("168.0.0.0", "255.0.0.0"),
        ("169.0.0.0", "255.128.0.0"),
        ("169.128.0.0", "255.192.0.0"),
        ("169.192.0.0", "255.224.0.0"),
        ("169.224.0.0", "255.240.0.0"),
        ("169.240.0.0", "255.248.0.0"),
        ("169.248.0.0", "255.252.0.0"),
        ("169.252.0.0", "255.254.0.0"),
        ("169.255.0.0", "255.255.0.0"),
        ("170.0.0.0", "254.0.0.0"),
        ("172.0.0.0", "255.240.0.0"),
        ("172.32.0.0", "255.224.0.0"),
        ("172.64.0.0", "255.192.0.0"),
        ("172.128.0.0", "255.128.0.0"),
        ("173.0.0.0", "255.0.0.0"),
        ("174.0.0.0", "254.0.0.0"),
        ("176.0.0.0", "240.0.0.0"),
        ("192.0.0.0", "255.128.0.0"),
        ("192.128.0.0", "255.224.0.0"),
        ("192.160.0.0", "255.248.0.0"),
        ("192.169.0.0", "255.255.0.0"),
        ("192.170.0.0", "255.254.0.0"),
        ("192.172.0.0", "255.252.0.0"),
        ("192.176.0.0", "255.240.0.0"),
        ("192.192.0.0", "255.192.0.0"),
        ("193.0.0.0", "255.0.0.0"),
        ("194.0.0.0", "254.0.0.0"),
        ("196.0.0.0", "252.0.0.0"),
        ("200.0.0.0", "248.0.0.0"),
        ("208.0.0.0", "240.0.0.0"),
    ].map { NEIPv4Route(destinationAddress: $0.0, subnetMask: $0.1) }
}
