package dev.`fun`.revtether

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.IpPrefix
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import androidx.core.app.NotificationCompat
import java.io.BufferedInputStream
import java.io.FileInputStream
import java.io.FileOutputStream
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.Socket
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit
import kotlin.concurrent.thread

class TunnelService : VpnService() {
    @Volatile private var running = false
    private var tun: ParcelFileDescriptor? = null
    private var tunIn: FileInputStream? = null
    private var tunOut: FileOutputStream? = null
    @Volatile private var sock: Socket? = null
    private var server: ServerSocket? = null
    private val writeLock = Any()
    private val scheduler = Executors.newSingleThreadScheduledExecutor()
    private var keepalive: ScheduledFuture<*>? = null
    private var connectivity: ConnectivityManager? = null
    private var underlyingCallback: ConnectivityManager.NetworkCallback? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // stopService() cannot destroy a bound VpnService; close the TUN from here.
        if (intent?.action == ACTION_STOP) {
            shutdown()
            return START_NOT_STICKY
        }
        if (running) {
            return START_STICKY
        }
        val notification = buildNotification(STATE_WAITING)
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(
                NOTIF_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_SYSTEM_EXEMPTED,
            )
        } else {
            startForeground(NOTIF_ID, notification)
        }
        val b = Builder()
            .setSession(getString(R.string.vpn_session))
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
        // Play Store auto-updates wait for NOT_METERED. Default VPN is metered.
        if (Build.VERSION.SDK_INT >= 29) {
            b.setMetered(false)
        }
        runCatching { b.addDisallowedApplication(packageName) }
        Prefs.disallowedApps(this).forEach {
            runCatching { b.addDisallowedApplication(it) }
        }
        val pfd = runCatching { b.establish() }.onFailure {
            Log.e(TAG, "VpnService.establish failed", it)
        }.getOrNull() ?: run {
            Log.e(TAG, "VpnService.establish returned null")
            shutdown()
            return START_NOT_STICKY
        }
        tun = pfd
        tunIn = FileInputStream(pfd.fileDescriptor)
        tunOut = FileOutputStream(pfd.fileDescriptor)
        running = true
        watchUnderlyingNetworks()
        publish(STATE_WAITING)
        thread(name = "tun-reader", start = true) { tunReader() }
        thread(name = "server", start = true) {
            runCatching { serverLoop() }.onFailure {
                Log.e(TAG, "listener failed", it)
                shutdown()
            }
        }
        Log.i(TAG, "tunnel started, listening 127.0.0.1:31416")
        return START_STICKY
    }

    override fun onRevoke() {
        shutdown()
        super.onRevoke()
    }

    override fun onDestroy() {
        shutdown()
        scheduler.shutdownNow()
        super.onDestroy()
    }

    @Synchronized
    private fun shutdown() {
        running = false
        keepalive?.cancel(true)
        keepalive = null
        stopWatchingUnderlying()
        runCatching { sock?.close() }
        sock = null
        runCatching { server?.close() }
        server = null
        runCatching { tun?.close() }
        tun = null
        tunIn = null
        tunOut = null
        publish(STATE_OFF)
        @Suppress("DEPRECATION")
        stopForeground(true)
        stopSelf()
        Log.i(TAG, "tunnel stopped")
    }

    private fun serverLoop() {
        val srv = ServerSocket().apply {
            reuseAddress = true
            bind(InetSocketAddress(InetAddress.getByName("127.0.0.1"), 31416), 1)
        }
        server = srv
        srv.use {
            while (running) {
                val s = try {
                    srv.accept()
                } catch (e: Exception) {
                    if (running) throw e
                    break
                }
                runCatching {
                    s.tcpNoDelay = true
                    protect(s)
                    sock = s
                    sendHello(s)
                    Log.i(TAG, "host connected ${s.remoteSocketAddress}")
                    publish(STATE_CONNECTED)
                    startKeepalive()
                    sockReader(s)
                }.onFailure {
                    Log.i(TAG, "host disconnected: ${it.message}")
                }
                keepalive?.cancel(true)
                keepalive = null
                runCatching { s.close() }
                sock = null
                if (running) {
                    publish(STATE_WAITING)
                }
            }
        }
    }

    private fun watchUnderlyingNetworks() {
        if (Build.VERSION.SDK_INT < 22) return
        val cm = getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        connectivity = cm
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = publishUnderlying()
            override fun onLost(network: Network) = publishUnderlying()
            override fun onCapabilitiesChanged(network: Network, networkCapabilities: NetworkCapabilities) {
                publishUnderlying()
            }
        }
        underlyingCallback = cb
        val req = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .build()
        runCatching { cm.registerNetworkCallback(req, cb) }
            .onFailure { Log.w(TAG, "registerNetworkCallback failed", it) }
        publishUnderlying()
    }

    private fun stopWatchingUnderlying() {
        val cm = connectivity ?: return
        underlyingCallback?.let { runCatching { cm.unregisterNetworkCallback(it) } }
        underlyingCallback = null
        connectivity = null
    }

    // Android forces a VPN metered when it has no underlying NetworkAgent (Wi-Fi/cell/BT/ethernet).
    // Play's DownloadManager then waits forever on NOT_METERED, even if the user chose "any network".
    private fun publishUnderlying() {
        if (Build.VERSION.SDK_INT < 22) return
        val cm = connectivity ?: return
        val nets = cm.allNetworks.filter { n ->
            val caps = cm.getNetworkCapabilities(n) ?: return@filter false
            !caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN) &&
                caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
        }.sortedByDescending { n ->
            val caps = cm.getNetworkCapabilities(n)
            when {
                caps?.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED) == true -> 2
                else -> 1
            }
        }
        val ok = setUnderlyingNetworks(if (nets.isEmpty()) null else nets.toTypedArray())
        Log.i(TAG, "underlying networks=${nets.size} set=$ok")
    }

    private fun sendHello(s: Socket) {
        synchronized(writeLock) {
            Frame.write(s.getOutputStream(), TYPE_HELLO, Frame.hello())
        }
    }

    private fun startKeepalive() {
        keepalive?.cancel(true)
        keepalive = scheduler.scheduleWithFixedDelay({
            val s = sock ?: return@scheduleWithFixedDelay
            runCatching {
                synchronized(writeLock) {
                    Frame.write(s.getOutputStream(), TYPE_KEEPALIVE, ByteArray(0))
                }
            }
        }, 5, 5, TimeUnit.SECONDS)
    }

    private fun tunReader() {
        val input = tunIn ?: return
        val buf = ByteArray(32767)
        var logged = 0
        while (running) {
            val n = runCatching { input.read(buf) }.getOrElse {
                Log.e(TAG, "tun read failed", it)
                return
            }
            if (n < 0) return
            if (n == 0) continue
            if ((buf[0].toInt() and 0xf0) != 0x40) {
                if (logged < 5) {
                    Log.w(TAG, "drop non-ipv4 n=$n b0=${buf[0].toInt() and 0xff}")
                    logged++
                }
                continue
            }
            val s = sock
            if (s == null) continue
            if (logged < 8) {
                Log.i(TAG, "tun->host n=$n proto=${buf[9].toInt() and 0xff}")
                logged++
            }
            runCatching {
                synchronized(writeLock) {
                    Frame.write(s.getOutputStream(), TYPE_IP, buf, n)
                }
            }.onFailure {
                Log.w(TAG, "tun->host write failed: ${it.message}")
            }
        }
    }

    private fun sockReader(s: Socket) {
        val input = BufferedInputStream(s.getInputStream(), 64 * 1024)
        val out = tunOut ?: return
        s.soTimeout = 15_000
        while (running) {
            val (type, payload) = Frame.read(input)
            when (type) {
                TYPE_IP -> {
                    out.write(payload)
                    out.flush()
                }
            }
        }
    }

    private fun buildNotification(forState: Int = state): Notification {
        val channelId = "revtether"
        if (Build.VERSION.SDK_INT >= 26) {
            val mgr = getSystemService(NotificationManager::class.java)
            mgr.createNotificationChannel(
                NotificationChannel(channelId, getString(R.string.notif_channel), NotificationManager.IMPORTANCE_LOW)
            )
        }
        val pending = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )
        return NotificationCompat.Builder(this, channelId)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(statusText(forState))
            .setSmallIcon(android.R.drawable.stat_sys_upload)
            .setContentIntent(pending)
            .setOngoing(true)
            .build()
    }

    private fun statusText(forState: Int): String = getString(
        when (forState) {
            STATE_CONNECTED -> R.string.status_connected_title
            STATE_WAITING -> R.string.status_waiting_title
            else -> R.string.status_idle_title
        }
    )

    private fun publish(next: Int) {
        state = next
        val mgr = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (next == STATE_OFF) {
            mgr.cancel(NOTIF_ID)
        } else {
            mgr.notify(NOTIF_ID, buildNotification(next))
        }
        sendBroadcast(
            Intent(ACTION_STATE)
                .setPackage(packageName)
                .putExtra(EXTRA_STATE, next),
        )
    }

    companion object {
        const val NOTIF_ID = 17
        const val ACTION_STATE = "dev.fun.revtether.STATE"
        const val ACTION_STOP = "dev.fun.revtether.STOP"
        const val EXTRA_STATE = "state"
        const val STATE_OFF = 0
        const val STATE_WAITING = 1
        const val STATE_CONNECTED = 2
        private const val TAG = "ReverseTether"

        @Volatile
        var state: Int = STATE_OFF
            private set

        fun stop(context: Context) {
            context.startService(Intent(context, TunnelService::class.java).setAction(ACTION_STOP))
        }
    }
}
