import frappe

DEFAULT_NOTE = "Offline install"


def after_install():
	"""Create a default record in this app's own DocType.

	This is the shape that broke on a real bench (issue #13): an install hook that
	writes to a DocType the same install is supposed to have synced a moment earlier.
	When the sync silently does nothing, this raises and leaves the site with the app
	registered and none of its tables — so the fixture only installs cleanly if the
	DocTypes really reached the database.
	"""
	if frappe.db.exists("FPM Demo Note", DEFAULT_NOTE):
		return
	frappe.get_doc(
		{
			"doctype": "FPM Demo Note",
			"title": DEFAULT_NOTE,
			"body": "created by fpm_demo_base.install.after_install",
		}
	).insert(ignore_permissions=True)
