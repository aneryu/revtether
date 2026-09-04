package dev.fun.revtether

import android.content.Context

object Prefs {
    private const val NAME = "revtether"
    private const val KEY_DISALLOWED = "disallowed_apps"

    fun disallowedApps(ctx: Context): Set<String> {
        return ctx.getSharedPreferences(NAME, Context.MODE_PRIVATE)
            .getStringSet(KEY_DISALLOWED, emptySet())
            ?.toSet()
            ?: emptySet()
    }

    fun setDisallowedApps(ctx: Context, pkgs: Set<String>) {
        ctx.getSharedPreferences(NAME, Context.MODE_PRIVATE)
            .edit()
            .putStringSet(KEY_DISALLOWED, pkgs)
            .apply()
    }
}
