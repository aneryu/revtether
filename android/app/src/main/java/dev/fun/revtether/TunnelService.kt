package dev.fun.revtether

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.net.IpPrefix
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import androidx.core.app.NotificationCompat
import java.io.BufferedInputStream
import java.io.FileInputStream
import java.io.FileOutputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit
import kotlin.concurrent.thread

class TunnelService : VpnService() {
    @Volatile private var running = false
    private var tun: ParcelFileDescriptor? = null
    @Volatile private var sock: Socket? = null
    private val writeLock = Any()
    private val scheduler = Executors.newSingleThreadScheduledExecutor()
    private var keepalive: ScheduledFuture<*>? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (running) {
            return START_STICKY
        }
        startForeground(NOTIF_ID, buildNotification("等待电脑连接"))
        val b = Builder()
            .setSession("USB Reverse Tethering")
            .setMtu(1400)
            .addAddress("198.18.0.2", 24)
            .addDnsServer("198.18.0.1")
            .setBlocking(true)
        if (Build.VERSION.SDK_INT >= 33) {
            b.addRoute("0.0.0.0", 0)
            LAN_PREFIXES.forEach { cidr ->
                val (addr, prefix) = cidr.split("/")
                b.excludeRoute(IpPrefix(InetAddress.getByName(addr), prefix.toInt()))
            }
        } else {
            ROUTES_EXCLUDING_LAN.forEach { (a, p) -> b.addRoute(a, p) }
        }
        Prefs.disallowedApps(this).forEach {
            runCatching { b.addDisallowedApplication(it) }
        }
        tun = b.establish() ?: run {
            stopSelf()
            return START_NOT_STICKY
        }
        running = true
        thread(name = "tun-reader", start = true) { tunReader() }
        thread(name = "server", start = true) { serverLoop() }
        return START_STICKY
    }

    override fun onDestroy() {
        running = false
        keepalive?.cancel(true)
        scheduler.shutdownNow()
        runCatching { sock?.close() }
        runCatching { tun?.close() }
        super.onDestroy()
    }

    private fun serverLoop() {
        ServerSocket(31416, 1, InetAddress.getLoopbackAddress()).use { server ->
            while (running) {
                val s = runCatching { server.accept() }.getOrNull() ?: continue
                s.tcpNoDelay = true
                sock = s
                updateNotification("已连接")
                sendHello(s)
                startKeepalive()
                runCatching { sockReader(s) }
                keepalive?.cancel(true)
                keepalive = null
                runCatching { s.close() }
                sock = null
                updateNotification("等待电脑连接")
            }
        }
    }

    private fun sendHello(s: Socket) {
        synchronized(writeLock) {
            Frame.write(s.getOutputStream(), TYPE_HELLO, Frame.hello())
        }
    }

    private fun startKeepalive() {
        keepalive?.cancel(true)
        keepalive = scheduler.scheduleAtFixedRate({
            val s = sock ?: return@scheduleAtFixedRate
            runCatching {
                synchronized(writeLock) {
                    Frame.write(s.getOutputStream(), TYPE_KEEPALIVE, ByteArray(0))
                }
            }
        }, 5, 5, TimeUnit.SECONDS)
    }

    private fun tunReader() {
        val input = FileInputStream(tun!!.fileDescriptor)
        val buf = ByteArray(1500)
        while (running) {
            val n = input.read(buf)
            if (n <= 0) continue
            if (buf[0].toInt().ushr(4) != 4) continue
            val s = sock ?: continue
            runCatching {
                synchronized(writeLock) {
                    Frame.write(s.getOutputStream(), TYPE_IP, buf, n)
                }
            }
        }
    }

    private fun sockReader(s: Socket) {
        val input = BufferedInputStream(s.getInputStream(), 64 * 1024)
        val out = FileOutputStream(tun!!.fileDescriptor)
        s.soTimeout = 15_000
        while (running) {
            val (type, payload) = Frame.read(input)
            when (type) {
                TYPE_IP -> out.write(payload)
            }
        }
    }

    private fun buildNotification(text: String): Notification {
        val channelId = "revtether"
        if (Build.VERSION.SDK_INT >= 26) {
            val mgr = getSystemService(NotificationManager::class.java)
            mgr.createNotificationChannel(
                NotificationChannel(channelId, "Reverse Tether", NotificationManager.IMPORTANCE_LOW)
            )
        }
        val pending = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )
        return NotificationCompat.Builder(this, channelId)
            .setContentTitle("USB Reverse Tethering")
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_upload)
            .setContentIntent(pending)
            .setOngoing(true)
            .build()
    }

    private fun updateNotification(text: String) {
        val mgr = getSystemService(NotificationManager::class.java)
        mgr.notify(NOTIF_ID, buildNotification(text))
    }

    companion object {
        const val NOTIF_ID = 17
    }
}
