package dev.fun.revtether

import android.app.Activity
import android.content.Intent
import android.net.VpnService
import android.os.Bundle
import android.widget.Button
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat

class MainActivity : AppCompatActivity() {
    private lateinit var status: TextView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        status = findViewById(R.id.status)
        findViewById<Button>(R.id.btnStart).setOnClickListener { prepareVpn() }
        findViewById<Button>(R.id.btnStop).setOnClickListener { stopTunnel() }
        findViewById<Button>(R.id.btnExclude).setOnClickListener {
            startActivity(Intent(this, ExcludeAppsActivity::class.java))
        }
        prepareVpn()
    }

    private fun prepareVpn() {
        val intent = VpnService.prepare(this)
        if (intent != null) {
            startActivityForResult(intent, REQ_VPN)
        } else {
            startTunnel()
        }
    }

    @Deprecated("Deprecated in Java")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == REQ_VPN && resultCode == Activity.RESULT_OK) {
            startTunnel()
        }
    }

    private fun startTunnel() {
        ContextCompat.startForegroundService(this, Intent(this, TunnelService::class.java))
        status.text = "已开启，等待电脑运行 revtether run"
    }

    private fun stopTunnel() {
        stopService(Intent(this, TunnelService::class.java))
        status.text = "未连接"
    }

    companion object {
        private const val REQ_VPN = 1
    }
}
