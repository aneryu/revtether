import Foundation

enum L10n {
    static var appName: String { t("app_name", "Reverse Tether") }

    static var statusIdleTitle: String { t("status_idle_title", "Off") }
    static var statusStartingTitle: String { t("status_starting_title", "Preparing…") }
    static var statusWaitingTitle: String { t("status_waiting_title", "Waiting for computer") }
    static var statusConnectedTitle: String { t("status_connected_title", "Using computer network") }
    static var statusFailedTitle: String { t("status_failed_title", "Couldn't start") }

    static var statusIdleDetail: String { t("status_idle_detail", "Tap Start to use the computer's internet over USB.") }
    static var statusStartingDetail: String { t("status_starting_detail", "If a prompt appears, tap Allow.") }
    static var statusConnectedDetail: String { t("status_connected_detail", "Keep the USB cable plugged in.") }

    static var stepUsb: String { t("step_usb", "Plug in the USB cable") }
    static var stepComputer: String { t("step_computer", "Open Reverse Tether on the computer") }

    static var actionStart: String { t("action_start", "Start") }
    static var actionStarting: String { t("action_starting", "Preparing") }
    static var actionStop: String { t("action_stop", "Stop") }
    static var actionCancel: String { t("action_cancel", "Cancel") }
    static var actionOpenSettings: String { t("action_open_settings", "Open Settings") }

    static var errorNoNetwork: String { t("error_no_network", "Turn on Cellular or Wi-Fi first (you don't need to join a network), then try again.") }
    static var errorConfigInvalid: String { t("error_config_invalid", "Delete the old Reverse Tether VPN in Settings, then try again.") }
    static var errorConfigDisabled: String { t("error_config_disabled", "Turn on Reverse Tether in Settings › VPN.") }
    static var errorConnectionFailed: String { t("error_connection_failed", "When asked, tap Allow.") }
    static var errorConfigStale: String { t("error_config_stale", "Tap Start again.") }

    static var vpnSession: String { t("vpn_session", "Reverse Tether") }

    private static func t(_ key: String, _ fallback: String) -> String {
        let value = NSLocalizedString(key, tableName: "Localizable", bundle: .main, value: fallback, comment: "")
        return value.isEmpty ? fallback : value
    }
}
