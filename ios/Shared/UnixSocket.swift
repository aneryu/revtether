import Darwin
import Foundation

enum UnixSocket {
    static func listenTCP(port: UInt16, loopback: Bool, backlog: Int32 = 16) -> Int32? {
        let fd = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP)
        guard fd >= 0 else { return nil }
        var yes: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &yes, socklen_t(MemoryLayout<Int32>.size))
        setNoSigPipe(fd)
        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = port.bigEndian
        addr.sin_addr = in_addr(s_addr: loopback ? INADDR_LOOPBACK.bigEndian : INADDR_ANY)
        let bound = withUnsafePointer(to: &addr) { ptr in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        if bound != 0 {
            Darwin.close(fd)
            return nil
        }
        if Darwin.listen(fd, backlog) != 0 {
            Darwin.close(fd)
            return nil
        }
        return fd
    }

    static func accept(_ listenFd: Int32) -> Int32? {
        var addr = sockaddr_storage()
        var len = socklen_t(MemoryLayout<sockaddr_storage>.size)
        let fd = withUnsafeMutablePointer(to: &addr) { ptr in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.accept(listenFd, $0, &len)
            }
        }
        guard fd >= 0 else { return nil }
        // The listener binds all interfaces for usbmux, but only the device's
        // own proxy may connect. Never hand the tunnel to a LAN client.
        let localPeer = withUnsafePointer(to: &addr) { ptr in
            ptr.withMemoryRebound(to: sockaddr_in.self, capacity: 1) {
                $0.pointee.sin_family == sa_family_t(AF_INET) &&
                    isLocalIPv4($0.pointee.sin_addr.s_addr)
            }
        }
        guard localPeer else {
            Darwin.close(fd)
            return nil
        }
        setNoSigPipe(fd)
        var yes: Int32 = 1
        setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &yes, socklen_t(MemoryLayout<Int32>.size))
        return fd
    }

    private static func isLocalIPv4(_ address: in_addr_t) -> Bool {
        if address.bigEndian >> 24 == 127 { return true }
        var interfaces: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&interfaces) == 0 else { return false }
        defer { freeifaddrs(interfaces) }
        var current = interfaces
        while let iface = current {
            if let addr = iface.pointee.ifa_addr, addr.pointee.sa_family == sa_family_t(AF_INET) {
                let matches = addr.withMemoryRebound(to: sockaddr_in.self, capacity: 1) {
                    $0.pointee.sin_addr.s_addr == address
                }
                if matches { return true }
            }
            current = iface.pointee.ifa_next
        }
        return false
    }

    static func close(_ fd: Int32) {
        Darwin.close(fd)
    }

    private static func setNoSigPipe(_ fd: Int32) {
        var yes: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &yes, socklen_t(MemoryLayout<Int32>.size))
    }
}
