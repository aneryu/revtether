package dev.`fun`.revtether

import android.app.Activity
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.net.VpnService
import android.os.Bundle
import android.view.View
import android.view.WindowManager
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.view.WindowCompat
import com.google.android.material.button.MaterialButton

class MainActivity : AppCompatActivity() {
    private lateinit var statusTitle: TextView
    private lateinit var statusDetail: TextView
    private lateinit var statusDot: ImageView
    private lateinit var steps: LinearLayout
    private lateinit var btnPrimary: MaterialButton
    private lateinit var btnStop: MaterialButton

    private var starting = false
    private var failedMessage: String? = null

    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            if (intent?.getIntExtra(TunnelService.EXTRA_STATE, TunnelService.STATE_OFF) == TunnelService.STATE_OFF) {
                starting = false
            }
            render()
        }
    }

    private val vpnPermission = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult(),
    ) { result ->
        if (result.resultCode == Activity.RESULT_OK) {
            startTunnel()
        } else {
            starting = false
            failedMessage = getString(R.string.error_vpn_denied)
            render()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        applyLightSystemBars()
        statusTitle = findViewById(R.id.statusTitle)
        statusDetail = findViewById(R.id.statusDetail)
        statusDot = findViewById(R.id.statusDot)
        steps = findViewById(R.id.steps)
        btnPrimary = findViewById(R.id.btnPrimary)
        btnStop = findViewById(R.id.btnStop)
        btnPrimary.setOnClickListener { if (!starting) prepareVpn() }
        btnStop.setOnClickListener { stopTunnel() }
        findViewById<MaterialButton>(R.id.btnExclude).setOnClickListener {
            startActivity(Intent(this, ExcludeAppsActivity::class.java))
        }
        prepareVpn()
    }

    override fun onResume() {
        super.onResume()
        ContextCompat.registerReceiver(
            this,
            receiver,
            IntentFilter(TunnelService.ACTION_STATE),
            ContextCompat.RECEIVER_NOT_EXPORTED,
        )
        render()
    }

    override fun onPause() {
        runCatching { unregisterReceiver(receiver) }
        super.onPause()
    }

    private fun prepareVpn() {
        starting = true
        failedMessage = null
        render()
        val intent = VpnService.prepare(this)
        if (intent != null) {
            vpnPermission.launch(intent)
        } else {
            startTunnel()
        }
    }

    private fun startTunnel() {
        ContextCompat.startForegroundService(this, Intent(this, TunnelService::class.java))
        render()
    }

    private fun stopTunnel() {
        starting = false
        failedMessage = null
        TunnelService.stop(this)
        render()
    }

    private fun render() {
        val state = TunnelService.state
        if (state != TunnelService.STATE_OFF) {
            starting = false
            failedMessage = null
        }

        val waiting = state == TunnelService.STATE_WAITING
        val connected = state == TunnelService.STATE_CONNECTED
        val preparing = starting && state == TunnelService.STATE_OFF
        val failed = failedMessage != null && state == TunnelService.STATE_OFF && !starting

        statusTitle.text = when {
            preparing -> getString(R.string.status_starting_title)
            connected -> getString(R.string.status_connected_title)
            waiting -> getString(R.string.status_waiting_title)
            failed -> getString(R.string.status_failed_title)
            else -> getString(R.string.status_idle_title)
        }
        statusDetail.text = when {
            preparing -> getString(R.string.status_starting_detail)
            connected -> getString(R.string.status_connected_detail)
            failed -> failedMessage
            else -> getString(R.string.status_idle_detail)
        }
        val dot = when {
            preparing || waiting -> R.color.status_waiting
            connected -> R.color.status_connected
            failed -> R.color.status_failed
            else -> R.color.status_idle
        }
        statusDot.imageTintList = ContextCompat.getColorStateList(this, dot)

        steps.visibility = if (waiting) View.VISIBLE else View.GONE
        statusDetail.visibility = if (waiting) View.GONE else View.VISIBLE

        if (waiting || connected) {
            btnPrimary.visibility = View.GONE
            btnStop.visibility = View.VISIBLE
            btnStop.text = getString(
                if (connected) R.string.action_stop else R.string.action_cancel,
            )
        } else {
            btnStop.visibility = View.GONE
            btnPrimary.visibility = View.VISIBLE
            btnPrimary.isEnabled = !preparing
            btnPrimary.alpha = if (preparing) 0.6f else 1f
            btnPrimary.text = getString(
                if (preparing) R.string.action_starting else R.string.action_start,
            )
        }

        if (connected) {
            window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        } else {
            window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        }
    }
}

fun AppCompatActivity.applyLightSystemBars() {
    WindowCompat.getInsetsController(window, window.decorView).apply {
        isAppearanceLightStatusBars = true
        isAppearanceLightNavigationBars = true
    }
}
