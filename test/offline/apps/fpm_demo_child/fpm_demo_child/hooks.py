app_name = "fpm_demo_child"
app_title = "FPM Demo Child"
app_publisher = "FPM offline test"
app_description = "FPM Demo Child (fpm offline integration fixture)"
app_email = "dev@example.com"
app_license = "mit"
# Resolved by fpm package to a pinned org/app==version; the offline install
# refuses to start unless that package is already in the local FPM store.
required_apps = [
	"frappe",
	"fpmtest/fpm_demo_base",  # qualified with the publishing org
]
# Desk (logged-in) pages load these through sites/assets/assets.json.
app_include_js = ["fpm_demo_child.bundle.js"]
app_include_css = ["fpm_demo_child.bundle.css"]
# Website pages (e.g. /login) load these the same way.
web_include_js = ["fpm_demo_child.bundle.js"]
web_include_css = ["fpm_demo_child.bundle.css"]

