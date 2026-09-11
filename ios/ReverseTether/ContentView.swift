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
    static var noNetworkHint: String { L10n.errorNoNetwork }

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
        HostRelay.shared.onDropped = { [weak self] in
            Task { @MainActor in
                self?.hostLinked = false
                self?.refreshStatus()
            }
        }
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
        StayAlive.start()

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
        StayAlive.stop()
        HostRelay.shared.setSession(nil)
        HostRelay.shared.stop()
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
                return L10n.errorConfigInvalid
            case .configurationDisabled:
                return L10n.errorConfigDisabled
            case .connectionFailed:
                return L10n.errorConnectionFailed
            case .configurationStale:
                return L10n.errorConfigStale
            default:
                break
            }
        }
        return error.localizedDescription
    }
}
private enum TunnelPreferences {
    static let providerID = "dev.fun.revtether.tunnel"
    static var displayName: String { L10n.vpnSession }

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
                colors: [Palette.bgTop, Palette.bgBottom],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            )
            .ignoresSafeArea()

            VStack(spacing: 0) {
                Spacer(minLength: 28)

                Text(L10n.appName)
                    .font(.custom("Avenir Next", size: 20).weight(.semibold))
                    .tracking(0.8)
                    .foregroundStyle(Palette.title)
                    .padding(.bottom, 36)

                HStack(spacing: 8) {
                    Circle()
                        .fill(statusColor)
                        .frame(width: 8, height: 8)
                    Text(statusTitle)
                        .font(.custom("Avenir Next", size: 17).weight(.semibold))
                        .foregroundStyle(Palette.status)
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 10)
                .background(Capsule().fill(Palette.pill))
                .padding(.bottom, 20)

                if tunnel.state == .waiting {
                    VStack(alignment: .leading, spacing: 14) {
                        StepRow(number: 1, text: L10n.stepUsb)
                        StepRow(number: 2, text: L10n.stepComputer)
                    }
                    .frame(maxWidth: 300, alignment: .leading)
                    .padding(.horizontal, 28)
                } else {
                    Text(statusDetail)
                        .font(.custom("Avenir Next", size: 16))
                        .foregroundStyle(Palette.detail)
                        .multilineTextAlignment(.center)
                        .lineSpacing(4)
                        .padding(.horizontal, 36)
                        .frame(maxWidth: 320)
                }

                Spacer().frame(height: 36)

                if showsPrimaryAction {
                    Button(action: { tunnel.start() }) {
                        Text(primaryTitle)
                            .font(.custom("Avenir Next", size: 17).weight(.semibold))
                            .frame(width: 220)
                            .padding(.vertical, 14)
                            .background(Palette.accent)
                            .foregroundStyle(.white)
                            .clipShape(Capsule())
                            .shadow(color: Palette.accent.opacity(0.28), radius: 14, y: 8)
                    }
                    .disabled(tunnel.state == .starting)
                    .opacity(tunnel.state == .starting ? 0.6 : 1)
                } else {
                    Button(action: { tunnel.stop() }) {
                        Text(tunnel.state == .connected ? L10n.actionStop : L10n.actionCancel)
                            .font(.custom("Avenir Next", size: 16).weight(.medium))
                            .foregroundStyle(Palette.accent)
                    }
                }

                if case .failed = tunnel.state {
                    Button(L10n.actionOpenSettings) {
                        if let url = URL(string: UIApplication.openSettingsURLString) {
                            UIApplication.shared.open(url)
                        }
                    }
                    .font(.custom("Avenir Next", size: 15).weight(.medium))
                    .foregroundStyle(Palette.accent)
                    .padding(.top, 16)
                }

                Spacer(minLength: 32)
            }
        }
        .preferredColorScheme(.light)
        .onChange(of: tunnel.state) { state in
            UIApplication.shared.isIdleTimerDisabled = (state == .connected)
        }
        .onAppear {
            tunnel.start()
        }
    }

    private var showsPrimaryAction: Bool {
        switch tunnel.state {
        case .idle, .failed, .starting: return true
        case .waiting, .connected: return false
        }
    }

    private var statusTitle: String {
        switch tunnel.state {
        case .idle: return L10n.statusIdleTitle
        case .starting: return L10n.statusStartingTitle
        case .waiting: return L10n.statusWaitingTitle
        case .connected: return L10n.statusConnectedTitle
        case .failed: return L10n.statusFailedTitle
        }
    }

    private var statusDetail: String {
        switch tunnel.state {
        case .idle: return L10n.statusIdleDetail
        case .starting: return L10n.statusStartingDetail
        case .waiting: return ""
        case .connected: return L10n.statusConnectedDetail
        case .failed(let msg): return msg
        }
    }

    private var primaryTitle: String {
        tunnel.state == .starting ? L10n.actionStarting : L10n.actionStart
    }

    private var statusColor: Color {
        switch tunnel.state {
        case .idle: return Palette.idle
        case .starting, .waiting: return Palette.waiting
        case .connected: return Palette.connected
        case .failed: return Palette.failed
        }
    }
}

private struct StepRow: View {
    let number: Int
    let text: String

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Text("\(number)")
                .font(.custom("Avenir Next", size: 13).weight(.semibold))
                .foregroundStyle(Palette.accent)
                .frame(width: 24, height: 24)
                .background(Circle().fill(Palette.accent.opacity(0.12)))
            Text(text)
                .font(.custom("Avenir Next", size: 16))
                .foregroundStyle(Palette.detail)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
    }
}

private enum Palette {
    static let bgTop = Color(red: 0.93, green: 0.95, blue: 0.97)
    static let bgBottom = Color(red: 0.82, green: 0.88, blue: 0.86)
    static let title = Color(red: 0.08, green: 0.14, blue: 0.18)
    static let status = Color(red: 0.18, green: 0.32, blue: 0.30)
    static let detail = Color(red: 0.28, green: 0.36, blue: 0.38)
    static let accent = Color(red: 0.07, green: 0.42, blue: 0.40)
    static let pill = Color.white.opacity(0.45)
    static let idle = Color(red: 0.55, green: 0.60, blue: 0.62)
    static let waiting = Color(red: 0.85, green: 0.58, blue: 0.13)
    static let connected = Color(red: 0.13, green: 0.62, blue: 0.45)
    static let failed = Color(red: 0.75, green: 0.22, blue: 0.22)
}
