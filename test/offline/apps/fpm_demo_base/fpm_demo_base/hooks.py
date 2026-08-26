app_name = "fpm_demo_base"
app_title = "FPM Demo Base"
app_publisher = "FPM offline test"
app_description = "FPM Demo Base (fpm offline integration fixture)"
app_email = "dev@example.com"
app_license = "mit"
required_apps = ["frappe"]
# Desk (logged-in) pages load these through sites/assets/assets.json.
app_include_js = ["fpm_demo_base.bundle.js"]
app_include_css = ["fpm_demo_base.bundle.css"]
# Website pages (e.g. /login) load these the same way.
web_include_js = ["fpm_demo_base.bundle.js"]
web_include_css = ["fpm_demo_base.bundle.css"]

