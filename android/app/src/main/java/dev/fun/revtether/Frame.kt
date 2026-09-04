package dev.fun.revtether

import java.io.InputStream
import java.io.OutputStream

const val TYPE_HELLO: Int = 0x00
const val TYPE_IP: Int = 0x01
const val TYPE_KEEPALIVE: Int = 0x02
const val VERSION: Int = 1
const val PLATFORM_ANDROID: Int = 0
const val MAX_PAYLOAD: Int = 1500

object Frame {
    fun write(out: OutputStream, type: Int, payload: ByteArray, length: Int = payload.size) {
        require(length <= MAX_PAYLOAD)
        val hdr = byteArrayOf(
            type.toByte(),
            ((length shr 8) and 0xff).toByte(),
            (length and 0xff).toByte(),
        )
        out.write(hdr)
        if (length > 0) {
            out.write(payload, 0, length)
        }
        out.flush()
    }

    fun read(input: InputStream): Pair<Int, ByteArray> {
        val hdr = ByteArray(3)
        readFully(input, hdr)
        val type = hdr[0].toInt() and 0xff
        val n = ((hdr[1].toInt() and 0xff) shl 8) or (hdr[2].toInt() and 0xff)
        if (n > MAX_PAYLOAD) {
            throw IllegalArgumentException("frame too large: $n")
        }
        val payload = ByteArray(n)
        if (n > 0) {
            readFully(input, payload)
        }
        return type to payload
    }

    fun hello(): ByteArray {
        return byteArrayOf(VERSION.toByte(), PLATFORM_ANDROID.toByte(), 0, 0)
    }

    private fun readFully(input: InputStream, buf: ByteArray) {
        var off = 0
        while (off < buf.size) {
            val n = input.read(buf, off, buf.size - off)
            if (n < 0) {
                throw java.io.EOFException()
            }
            off += n
        }
    }
}
