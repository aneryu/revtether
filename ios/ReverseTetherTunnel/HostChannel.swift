import Darwin
import Foundation

final class HostChannel {
    private let fd: Int32
    private let queue: DispatchQueue
    private var readSource: DispatchSourceRead?
    private var closed = false

    var onBytes: ((Data) -> Void)?
    var onClose: (() -> Void)?

    init(fd: Int32, queue: DispatchQueue) {
        self.fd = fd
        self.queue = queue
    }

    func start() {
        let src = DispatchSource.makeReadSource(fileDescriptor: fd, queue: queue)
        src.setEventHandler { [weak self] in
            self?.readAvailable()
        }
        src.setCancelHandler { [fd] in
            Darwin.close(fd)
        }
        src.resume()
        readSource = src
    }

    func send(_ data: Data) {
        guard !data.isEmpty, !closed else { return }
        data.withUnsafeBytes { raw in
            guard let base = raw.bindMemory(to: UInt8.self).baseAddress else { return }
            var off = 0
            while off < data.count {
                let n = Darwin.write(fd, base + off, data.count - off)
                if n > 0 {
                    off += n
                    continue
                }
                if n < 0 && errno == EINTR {
                    continue
                }
                drop()
                return
            }
        }
    }

    func cancel() {
        closed = true
        readSource?.cancel()
        readSource = nil
    }

    private func readAvailable() {
        var buf = [UInt8](repeating: 0, count: 65536)
        let n = Darwin.read(fd, &buf, buf.count)
        if n > 0 {
            onBytes?(Data(buf[0..<n]))
            return
        }
        if n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR) {
            return
        }
        drop()
    }

    private func drop() {
        guard !closed else { return }
        closed = true
        onClose?()
        readSource?.cancel()
        readSource = nil
    }
}
