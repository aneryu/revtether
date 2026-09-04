import Foundation

enum TunnelIPC {
    static func encode(_ packets: [Data]) -> Data {
        var out = Data()
        for p in packets where !p.isEmpty && p.count <= 1500 {
            let n = UInt16(p.count)
            out.append(UInt8(n >> 8))
            out.append(UInt8(n & 0xff))
            out.append(p)
        }
        return out
    }

    static func decode(_ data: Data) -> [Data] {
        var packets: [Data] = []
        var i = 0
        let bytes = [UInt8](data)
        while i + 2 <= bytes.count {
            let n = Int(bytes[i]) << 8 | Int(bytes[i + 1])
            i += 2
            guard n > 0, i + n <= bytes.count else { break }
            packets.append(Data(bytes[i..<(i + n)]))
            i += n
        }
        return packets
    }
}
