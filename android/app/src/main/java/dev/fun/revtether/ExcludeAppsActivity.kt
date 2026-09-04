package dev.fun.revtether

import android.os.Bundle
import android.widget.ArrayAdapter
import android.widget.ListView
import androidx.appcompat.app.AppCompatActivity

class ExcludeAppsActivity : AppCompatActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val list = ListView(this)
        setContentView(list)

        val pm = packageManager
        val apps = pm.getInstalledApplications(0)
            .filter { pm.getLaunchIntentForPackage(it.packageName) != nil }
            .sortedBy { it.loadLabel(pm).toString() }
        val labels = apps.map { "${it.loadLabel(pm)} (${it.packageName})" }
        val selected = Prefs.disallowedApps(this).toMutableSet()

        val adapter = ArrayAdapter(this, android.R.layout.simple_list_item_multiple_choice, labels)
        list.choiceMode = ListView.CHOICE_MODE_MULTIPLE
        list.adapter = adapter
        apps.forEachIndexed { i, app ->
            list.setItemChecked(i, selected.contains(app.packageName))
        }
        list.setOnItemClickListener { _, _, position, _ ->
            val pkg = apps[position].packageName
            if (list.isItemChecked(position)) {
                selected.add(pkg)
            } else {
                selected.remove(pkg)
            }
            Prefs.setDisallowedApps(this, selected)
        }
    }
}
