import SwiftUI
import Network
import NetworkExtension
import os.log

enum TunnelUIState: Equatable {
    case idle
    case starting
    case waiting
    case connected
    case failed(String)
}

@MainActor
final class TunnelController: ObservableObject {
    @Published var state: TunnelUIState = .idle

    private var manager: NETunnelProviderManager?
    private var observer: NSObjectProtocol?
    private var hostUpObserver: NSObjectProtocol?
    private var hostDownObserver: NSObjectProtocol?
    private var starting = false
    private var pendingStart = false
    private var hostLinked = false
    private let log = OSLog(subsystem: "dev.fun.revtether", category: "app")
    static let noNetworkHint = "iOS 在没有可用网络时会直接掐掉 VPN。请打开蜂窝或 Wi-Fi（不必连上热点），然后再点开启。"

    init() {
        _ = DarwinHostObserver.shared
        observer = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange,
            object: nil,
            queue: .main
        ) { [weak self] note in
            Task { @MainActor in
                guard let self else { return }
                if let conn = note.object as? NEVPNConnection, conn !== self.manager?.connection {
                    return
                }
                self.refreshStatus()
            }
        }
        hostUpObserver = NotificationCenter.default.addObserver(
            forName: Notification.Name(AppGroup.hostUp),
            object: nil,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor in
                self?.hostLinked = true
                self?.refreshStatus()
            }
        }
        hostDownObserver = NotificationCenter.default.addObserver(
            forName: Notification.Name(AppGroup.hostDown),
            object: nil,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor in
                self?.hostLinked = false
                self?.refreshStatus()
            }
        }
        HostRelay.shared.onHandedOff = { [weak self] in
            Task { @MainActor in
                self?.hostLinked = true
                self?.refreshStatus()
            }
        }
        HostRelay.shared.start()
    }

    deinit {
        if let observer {
            NotificationCenter.default.removeObserver(observer)
        }
        if let hostUpObserver {
            NotificationCenter.default.removeObserver(hostUpObserver)
        }
        if let hostDownObserver {
            NotificationCenter.default.removeObserver(hostDownObserver)
        }
    }

    func start() {
        guard !starting else { return }
        starting = true
        state = .starting
        os_log("start tapped", log: log, type: .info)

        Task { [weak self] in
            guard let self else { return }
            do {
                if !(await NetworkPath.hasViableInterface()) {
                    os_log("no viable network path; iOS will reject the tunnel", log: self.log, type: .error)
                    self.state = .failed(Self.noNetworkHint)
                    self.starting = false
                    return
                }
                let prepared = try await TunnelPreferences.prepare()
                self.manager = prepared
                HostRelay.shared.setSession(prepared.connection as? NETunnelProviderSession)
                self.pendingStart = true
                HostRelay.shared.start()
                if prepared.connection.status == .disconnected || prepared.connection.status == .invalid {
                    try prepared.connection.startVPNTunnel()
                }
                self.refreshStatus()
                os_log("startVPNTunnel issued, status=%{public}d", log: self.log, type: .info, prepared.connection.status.rawValue)
            } catch {
                os_log("start failed: %{public}@", log: self.log, type: .error, error.localizedDescription)
                self.pendingStart = false
                self.state = .failed(Self.describe(error))
            }
            self.starting = false
        }
    }

    func stop() {
        pendingStart = false
        hostLinked = false
        HostRelay.shared.setSession(nil)
        manager?.connection.stopVPNTunnel()
        refreshStatus()
    }

    private func refreshStatus() {
        guard let status = manager?.connection.status else {
            if starting { return }
            if case .failed = state { return }
            state = .idle
            return
        }
        switch status {
        case .connected:
            pendingStart = false
            HostRelay.shared.setSession(manager?.connection as? NETunnelProviderSession)
            state = hostLinked ? .connected : .waiting
        case .connecting, .reasserting:
            state = .waiting
        case .disconnecting:
            state = pendingStart ? .starting : .waiting
        case .disconnected, .invalid:
            if pendingStart {
                pendingStart = false
                os_log("tunnel dropped immediately after start", log: log, type: .error)
                state = .failed(Self.noNetworkHint)
                return
            }
            if case .failed = state { return }
            state = .idle
        @unknown default:
            state = .waiting
        }
    }

    private static func describe(_ error: Error) -> String {
        let ne = error as NSError
        if ne.domain == NEVPNErrorDomain {
            switch NEVPNError.Code(rawValue: ne.code) {
            case .configurationInvalid:
                return "VPN 配置无效。请删掉设置里的旧 VPN 后再试。"
            case .configurationDisabled:
                return "VPN 配置被关闭，请在设置中启用。"
            case .connectionFailed:
                return "隧道启动失败。请确认已允许添加 VPN 配置。"
            case .configurationStale:
                return "配置已过期，请再点一次开启。"
            default:
                break
            }
        }
        return error.localizedDescription
    }
}

private enum TunnelPreferences {
    static let providerID = "dev.fun.revtether.tunnel"
    static let displayName = "USB Reverse Tethering"

    static func prepare() async throws -> NETunnelProviderManager {
        let existing = try await loadAll()
        let manager = existing.first(where: {
            ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == providerID
        }) ?? NETunnelProviderManager()

        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = providerID
        proto.serverAddress = "USB"
        proto.disconnectOnSleep = false
        manager.protocolConfiguration = proto
        manager.localizedDescription = displayName
        manager.isEnabled = true
        let connect = NEOnDemandRuleConnect()
        connect.interfaceTypeMatch = .any
        manager.onDemandRules = [connect]
        manager.isOnDemandEnabled = true

        try await save(manager)
        let reloaded = try await loadAll()
        if let live = reloaded.first(where: {
            ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == providerID
        }) {
            try await load(live)
            return live
        }
        try await load(manager)
        return manager
    }

    static func loadAll() async throws -> [NETunnelProviderManager] {
        try await withCheckedThrowingContinuation { cont in
            NETunnelProviderManager.loadAllFromPreferences { list, error in
                if let error {
                    cont.resume(throwing: error)
                } else {
                    cont.resume(returning: list ?? [])
                }
            }
        }
    }

    static func save(_ manager: NETunnelProviderManager) async throws {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            manager.saveToPreferences { error in
                if let error {
                    cont.resume(throwing: error)
                } else {
                    cont.resume()
                }
            }
        }
    }

    static func load(_ manager: NETunnelProviderManager) async throws {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            manager.loadFromPreferences { error in
                if let error {
                    cont.resume(throwing: error)
                } else {
                    cont.resume()
                }
            }
        }
    }
}

private enum NetworkPath {
    static func hasViableInterface() async -> Bool {
        await withCheckedContinuation { cont in
            let monitor = NWPathMonitor()
            let queue = DispatchQueue(label: "revtether.path")
            var resumed = false
            let finish: (Bool) -> Void = { value in
                guard !resumed else { return }
                resumed = true
                monitor.cancel()
                cont.resume(returning: value)
            }
            monitor.pathUpdateHandler = { path in
                finish(path.status == .satisfied)
            }
            monitor.start(queue: queue)
            queue.asyncAfter(deadline: .now() + 1.2) {
                finish(false)
            }
        }
    }
}

struct ContentView: View {
    @StateObject private var tunnel = TunnelController()

    var body: some View {
        ZStack {
            LinearGradient(
                colors: [
                    Color(red: 0.93, green: 0.95, blue: 0.97),
                    Color(red: 0.82, green: 0.88, blue: 0.86)
                ],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            )
            .ignoresSafeArea()

            VStack(spacing: 28) {
                VStack(spacing: -4) {
                    Text("REVERSE")
                    Text("TETHER")
                }
                .font(.custom("Avenir Next Condensed", size: 52).weight(.heavy))
                .foregroundStyle(Color(red: 0.08, green: 0.14, blue: 0.18))

                Text(statusTitle)
                    .font(.custom("Avenir Next", size: 20).weight(.medium))
                    .foregroundStyle(Color(red: 0.18, green: 0.32, blue: 0.30))

                Text(statusDetail)
                    .font(.custom("Avenir Next", size: 14))
                    .foregroundStyle(Color(red: 0.28, green: 0.36, blue: 0.38))
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 36)

                Button(action: {
                    switch tunnel.state {
                    case .idle, .failed, .starting:
                        tunnel.start()
                    case .waiting, .connected:
                        tunnel.stop()
                    }
                }) {
                    Text(buttonTitle)
                        .font(.custom("Avenir Next", size: 17).weight(.semibold))
                        .frame(maxWidth: 220)
                        .padding(.vertical, 14)
                        .background(Color(red: 0.07, green: 0.42, blue: 0.40))
                        .foregroundStyle(.white)
                        .clipShape(Capsule())
                }
                .disabled(tunnel.state == .starting)
                .opacity(tunnel.state == .starting ? 0.6 : 1)
                .padding(.top, 8)

                if case .failed = tunnel.state {
                    Button("打开系统设置") {
                        if let url = URL(string: UIApplication.openSettingsURLString) {
                            UIApplication.shared.open(url)
                        }
                    }
                    .font(.custom("Avenir Next", size: 15).weight(.medium))
                    .foregroundStyle(Color(red: 0.07, green: 0.42, blue: 0.40))
                }
            }
        }
        .onChange(of: tunnel.state) { state in
            UIApplication.shared.isIdleTimerDisabled = (state == .connected)
        }
        .onAppear {
            tunnel.start()
        }
    }

    private var statusTitle: String {
        switch tunnel.state {
        case .idle: return "未连接"
        case .starting: return "正在开启…"
        case .waiting: return "已开启，等待电脑"
        case .connected: return "已连接"
        case .failed: return "启动失败"
        }
    }

    private var statusDetail: String {
        switch tunnel.state {
        case .idle:
            return "点开启后会建立 USB 反向网络隧道"
        case .starting:
            return "如弹出系统对话框，请允许添加 VPN 配置"
        case .waiting:
            return "VPN 已开，等待电脑 revtether run。"
        case .connected:
            return "手机流量走电脑网络。访问电脑本机服务请用 http://198.18.0.1:端口"
        case .failed(let msg):
            return msg
        }
    }

    private var buttonTitle: String {
        switch tunnel.state {
        case .idle, .failed: return "开启"
        case .starting: return "开启中"
        default: return "停止"
        }
    }
}
