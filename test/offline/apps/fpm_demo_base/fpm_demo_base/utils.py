import msgpack
import tabulate


def demo_table():
	"""Exercise both vendored third-party dependencies."""
	rows = [["fpm", msgpack.packb({"offline": True}).hex()]]
	return tabulate.tabulate(rows, headers=["tool", "payload"])
