import Darwin
import Foundation
import NetworkExtension
import os.log

/// lockdownd can reach the container app, not the packet-tunnel extension.
/// This process accepts usbmux on :31416, speaks the Reverse Tether framing protocol,
/// and shuttles IP packets to the extension with sendProviderMessage.
final class HostRelay: @unchecked Sendable {
    static let shared = HostRelay()

    var onHandedOff: (() -> Void)?

    private let queue = DispatchQueue(label: "revtether.host-relay")
    private let log = OSLog(subsystem: "dev.fun.revtether", category: "relay")
    private var listenFd: Int32 = -1
    private var acceptSource: DispatchSourceRead?
    private var clientFd: Int32 = -1
    private var clientSource: DispatchSourceRead?
    private var rx = Data()
    private var uplink: [Data] = []
    private var ipcBusy = false
    private var pollTimer: DispatchSourceTimer?
    private var keepalive: DispatchSourceTimer?
    private weak var session: NETunnelProviderSession?

    private init() {}

    func setSession(_ session: NETunnelProviderSession?) {
        queue.async { self.session = session }
    }

    func start() {
        queue.async { [self] in
            guard listenFd < 0 else { return }
            guard let fd = UnixSocket.listenTCP(port: AppGroup.listenPort, loopback: false) else {
                os_log("listen *:31416 failed: %{public}d", log: log, type: .error, errno)
                return
            }
            listenFd = fd
            let src = DispatchSource.makeReadSource(fileDescriptor: fd, queue: queue)
            src.setEventHandler { [weak self] in
                self?.acceptOne()
            }
            src.resume()
            acceptSource = src
            os_log("listening on 0.0.0.0:31416 for usbmux", log: log, type: .info)
        }
    }

    func stop() {
        queue.async { [self] in
            dropClient()
            acceptSource?.cancel()
            acceptSource = nil
            if listenFd >= 0 {
                UnixSocket.close(listenFd)
                listenFd = -1
            }
        }
    }

    private func acceptOne() {
        guard let client = UnixSocket.accept(listenFd) else { return }
        os_log("usbmux accepted fd=%{public}d", log: log, type: .info, client)
        dropClient()
        clientFd = client
        rx.removeAll()
        uplink.removeAll()
        writeAll(Frame.hello(platform: 1))
        let src = DispatchSource.makeReadSource(fileDescriptor: client, queue: queue)
        src.setEventHandler { [weak self] in
            self?.readClient()
        }
        src.resume()
        clientSource = src
        startKeepalive()
        startPoll()
        DispatchQueue.main.async {
            StayAlive.start()
            self.onHandedOff?()
        }
        flushIPC()
    }

    private func readClient() {
        var buf = [UInt8](repeating: 0, count: 65536)
        let n = Darwin.read(clientFd, &buf, buf.count)
        if n > 0 {
            rx.append(contentsOf: buf[0..<n])
            drainFrames()
            return
        }
        if n < 0 && (errno == EINTR || errno == EAGAIN) {
            return
        }
        dropClient()
    }

    private func drainFrames() {
        while let f = Frame.next(from: &rx) {
            switch f.type {
            case .ip:
                uplink.append(f.payload)
            case .hello, .keepalive:
                break
            }
        }
        flushIPC()
    }

    private func flushIPC() {
        guard !ipcBusy, clientFd >= 0 else { return }
        guard let session else { return }
        ipcBusy = true
        let payload = TunnelIPC.encode(uplink)
        uplink.removeAll()
        do {
            try session.sendProviderMessage(payload) { [weak self] response in
                self?.queue.async {
                    guard let self else { return }
                    self.ipcBusy = false
                    if let response {
                        for pkt in TunnelIPC.decode(response) {
                            var frame = Data()
                            Frame.append(&frame, type: .ip, payload: pkt)
                            self.writeAll(frame)
                        }
                    }
                    if !self.uplink.isEmpty {
                        self.flushIPC()
                    }
                }
            }
        } catch {
            ipcBusy = false
        }
    }

    private func startPoll() {
        pollTimer?.cancel()
        let t = DispatchSource.makeTimerSource(queue: queue)
        t.schedule(deadline: .now() + 0.02, repeating: 0.02)
        t.setEventHandler { [weak self] in
            self?.flushIPC()
        }
        t.resume()
        pollTimer = t
    }

    private func startKeepalive() {
        keepalive?.cancel()
        let t = DispatchSource.makeTimerSource(queue: queue)
        t.schedule(deadline: .now() + 5, repeating: 5)
        t.setEventHandler { [weak self] in
            self?.writeAll(Frame.keepalive())
        }
        t.resume()
        keepalive = t
    }

    private func writeAll(_ data: Data) {
        guard clientFd >= 0, !data.isEmpty else { return }
        data.withUnsafeBytes { raw in
            guard let base = raw.bindMemory(to: UInt8.self).baseAddress else { return }
            var off = 0
            while off < data.count {
                let n = Darwin.write(self.clientFd, base + off, data.count - off)
                if n > 0 {
                    off += n
                    continue
                }
                if n < 0 && errno == EINTR { continue }
                self.dropClient()
                return
            }
        }
    }

    private func dropClient() {
        pollTimer?.cancel()
        pollTimer = nil
        keepalive?.cancel()
        keepalive = nil
        clientSource?.cancel()
        clientSource = nil
        if clientFd >= 0 {
            UnixSocket.close(clientFd)
            clientFd = -1
        }
        rx.removeAll()
        uplink.removeAll()
        ipcBusy = false
        DispatchQueue.main.async { StayAlive.stop() }
    }
}
