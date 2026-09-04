import Foundation

enum FrameType: UInt8 {
    case hello = 0x00
    case ip = 0x01
    case keepalive = 0x02
}

struct Frame {
    let type: FrameType
    let payload: Data

    static let maxPayload = 1500

    static func append(_ buf: inout Data, type: FrameType, payload: Data) {
        var hdr = Data(count: 3)
        hdr[0] = type.rawValue
        let n = UInt16(min(payload.count, maxPayload))
        hdr[1] = UInt8(n >> 8)
        hdr[2] = UInt8(n & 0xff)
        buf.append(hdr)
        buf.append(payload.prefix(Int(n)))
    }

    static func hello(platform: UInt8) -> Data {
        var buf = Data()
        append(&buf, type: .hello, payload: Data([1, platform, 0, 0]))
        return buf
    }

    static func keepalive() -> Data {
        var buf = Data()
        append(&buf, type: .keepalive, payload: Data())
        return buf
    }

    static func next(from buf: inout Data) -> Frame? {
        guard buf.count >= 3 else { return nil }
        let n = Int(buf[1]) << 8 | Int(buf[2])
        if n > maxPayload {
            buf.removeAll()
            return nil
        }
        guard buf.count >= 3 + n else { return nil }
        guard let type = FrameType(rawValue: buf[0]) else {
            buf.removeSubrange(0..<(3 + n))
            return nil
        }
        let payload = buf.subdata(in: 3..<(3 + n))
        buf.removeSubrange(0..<(3 + n))
        return Frame(type: type, payload: payload)
    }
}
