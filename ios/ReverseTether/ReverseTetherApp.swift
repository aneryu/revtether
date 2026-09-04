import SwiftUI
import NetworkExtension

@main
struct ReverseTetherApp: App {
    init() {
        HostRelay.shared.start()
    }

    var body: some Scene {
        WindowGroup {
            ContentView()
        }
    }
}
