import Foundation

enum AppGroup {
    static let id = "group.dev.fun.revtether"
    static let listenPort: UInt16 = 31416
    static let hostUp = "dev.fun.revtether.host.up"
    static let hostDown = "dev.fun.revtether.host.down"

    static var containerURL: URL? {
        FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: id)
    }

    static var socketPath: String? {
        containerURL?.appendingPathComponent("revtether.sock").path
    }

    static func postHost(connected: Bool) {
        let name = connected ? hostUp : hostDown
        CFNotificationCenterPostNotification(
            CFNotificationCenterGetDarwinNotifyCenter(),
            CFNotificationName(name as CFString),
            nil,
            nil,
            true
        )
    }
}

/// Bridges Darwin notify into NotificationCenter so the UI can observe host up/down.
final class DarwinHostObserver: @unchecked Sendable {
    static let shared = DarwinHostObserver()

    private init() {
        let center = CFNotificationCenterGetDarwinNotifyCenter()
        let observer = Unmanaged.passUnretained(self).toOpaque()
        CFNotificationCenterAddObserver(
            center,
            observer,
            { _, _, _, _, _ in
                NotificationCenter.default.post(name: Notification.Name(AppGroup.hostUp), object: nil)
            },
            AppGroup.hostUp as CFString,
            nil,
            .deliverImmediately
        )
        CFNotificationCenterAddObserver(
            center,
            observer,
            { _, _, _, _, _ in
                NotificationCenter.default.post(name: Notification.Name(AppGroup.hostDown), object: nil)
            },
            AppGroup.hostDown as CFString,
            nil,
            .deliverImmediately
        )
    }
}
