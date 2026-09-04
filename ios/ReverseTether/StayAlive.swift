import AVFoundation
import Foundation

/// Keeps the container app runnable after the user switches to Safari.
/// The usbmux splice currently lives in this process; if iOS suspends us,
/// revtether falls back to "waiting for app".
enum StayAlive {
    private static var player: AVAudioPlayer?

    static func start() {
        let session = AVAudioSession.sharedInstance()
        try? session.setCategory(.playback, options: [.mixWithOthers])
        try? session.setActive(true)
        if player == nil {
            player = try? AVAudioPlayer(data: silentWav)
            player?.numberOfLoops = -1
            player?.volume = 0.01
        }
        player?.play()
    }

    static func stop() {
        player?.stop()
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    private static var silentWav: Data {
        var d = Data()
        func u32(_ v: UInt32) { d.append(contentsOf: withUnsafeBytes(of: v.littleEndian, Array.init)) }
        func u16(_ v: UInt16) { d.append(contentsOf: withUnsafeBytes(of: v.littleEndian, Array.init)) }
        d.append(contentsOf: Array("RIFF".utf8))
        u32(36 + 8000)
        d.append(contentsOf: Array("WAVEfmt ".utf8))
        u32(16)
        u16(1)
        u16(1)
        u32(8000)
        u32(8000)
        u16(1)
        u16(8)
        d.append(contentsOf: Array("data".utf8))
        u32(8000)
        d.append(contentsOf: [UInt8](repeating: 128, count: 8000))
        return d
    }
}
