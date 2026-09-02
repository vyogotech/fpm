import frappe
import msgpack
import tabulate


def demo_table():
	"""Exercise both vendored third-party dependencies."""
	rows = [["fpm", msgpack.packb({"offline": True}).hex()]]
	return tabulate.tabulate(rows, headers=["tool", "payload"])


def demo_note():
	"""Read back what after_install wrote into this app's own DocType.

	It answers only if the install synced the DocType and then ran the hook — the
	two halves that came apart in issue #13, where the app was registered on the
	site and none of its tables existed.
	"""
	return frappe.db.get_value("FPM Demo Note", "Offline install", "body")
