package dev.`fun`.revtether

import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.os.Bundle
import android.text.Editable
import android.text.TextWatcher
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.BaseAdapter
import android.widget.CheckBox
import android.widget.EditText
import android.widget.ImageView
import android.widget.ListView
import android.widget.ProgressBar
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import com.google.android.material.appbar.MaterialToolbar
import kotlin.concurrent.thread

class ExcludeAppsActivity : AppCompatActivity() {
    private lateinit var toolbar: MaterialToolbar
    private lateinit var list: ListView
    private lateinit var progress: ProgressBar
    private lateinit var empty: TextView
    private lateinit var adapter: AppAdapter

    private val selected = linkedSetOf<String>()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_exclude_apps)
        applyLightSystemBars()
        toolbar = findViewById(R.id.toolbar)
        list = findViewById(R.id.list)
        progress = findViewById(R.id.progress)
        empty = findViewById(R.id.empty)
        toolbar.setNavigationOnClickListener { finish() }

        selected.addAll(Prefs.disallowedApps(this))
        adapter = AppAdapter()
        list.adapter = adapter
        list.setOnItemClickListener { _, _, position, _ ->
            val row = adapter.getItem(position)
            if (!selected.add(row.info.packageName)) {
                selected.remove(row.info.packageName)
            }
            Prefs.setDisallowedApps(this, selected)
            adapter.notifyDataSetChanged()
            renderSubtitle()
        }

        findViewById<EditText>(R.id.search).addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {}
            override fun afterTextChanged(s: Editable?) {
                adapter.filter(s?.toString().orEmpty())
            }
        })

        renderSubtitle()
        thread(name = "exclude-apps") {
            val pm = packageManager
            val rows = pm.getInstalledApplications(PackageManager.GET_META_DATA)
                .filter { pm.getLaunchIntentForPackage(it.packageName) != null }
                .map { AppRow(it, it.loadLabel(pm).toString()) }
                .sortedBy { it.label.lowercase() }
            runOnUiThread {
                if (isDestroyed) return@runOnUiThread
                progress.visibility = View.GONE
                adapter.replace(rows)
            }
        }
    }

    private fun renderSubtitle() {
        toolbar.subtitle = getString(R.string.exclude_selected, selected.size)
    }

    private data class AppRow(val info: ApplicationInfo, val label: String)

    private inner class AppAdapter : BaseAdapter() {
        private var all: List<AppRow> = emptyList()
        private var shown: List<AppRow> = emptyList()

        fun replace(rows: List<AppRow>) {
            all = rows
            filter(findViewById<EditText>(R.id.search).text?.toString().orEmpty())
        }

        fun filter(query: String) {
            val q = query.trim().lowercase()
            shown = if (q.isEmpty()) {
                all
            } else {
                all.filter {
                    it.label.lowercase().contains(q) || it.info.packageName.lowercase().contains(q)
                }
            }
            notifyDataSetChanged()
            empty.visibility = if (shown.isEmpty() && all.isNotEmpty()) View.VISIBLE else View.GONE
        }

        override fun getCount(): Int = shown.size
        override fun getItem(position: Int): AppRow = shown[position]
        override fun getItemId(position: Int): Long = shown[position].info.packageName.hashCode().toLong()
        override fun hasStableIds(): Boolean = true

        override fun getView(position: Int, convertView: View?, parent: ViewGroup): View {
            val view = convertView ?: LayoutInflater.from(parent.context)
                .inflate(R.layout.item_app, parent, false)
            val holder = (view.tag as? Holder) ?: Holder(view).also { view.tag = it }
            val row = shown[position]
            holder.icon.setImageDrawable(row.info.loadIcon(packageManager))
            holder.label.text = row.label
            holder.packageName.text = row.info.packageName
            holder.check.isChecked = selected.contains(row.info.packageName)
            return view
        }
    }

    private class Holder(view: View) {
        val icon: ImageView = view.findViewById(R.id.icon)
        val label: TextView = view.findViewById(R.id.label)
        val packageName: TextView = view.findViewById(R.id.packageName)
        val check: CheckBox = view.findViewById(R.id.check)
    }
}
