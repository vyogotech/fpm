#!/usr/bin/env python3
"""Tiny byte relay used to reach the network-isolated site from a browser on the LAN.

UNIX sockets are filesystem objects and cross network namespaces; nothing else does.
So one copy runs inside the isolated container's network namespace and forwards a
UNIX socket on a bind mount to 127.0.0.1:8000, and another runs on the host and
forwards a TCP port to that UNIX socket. The container itself gains no network.

    relay.py unix2tcp <socket-path> <host> <port>   # inside the container
    relay.py tcp2unix <port> <socket-path>          # on the host
"""
import os
import socket
import sys
import threading


def pump(src, dst):
    try:
        while True:
            data = src.recv(65536)
            if not data:
                break
            dst.sendall(data)
    except OSError:
        pass
    finally:
        for s in (src, dst):
            try:
                s.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass


def serve(listener, connect):
    while True:
        client, _ = listener.accept()
        try:
            upstream = connect()
        except OSError as exc:
            print("upstream connect failed:", exc, flush=True)
            client.close()
            continue
        threading.Thread(target=pump, args=(client, upstream), daemon=True).start()
        threading.Thread(target=pump, args=(upstream, client), daemon=True).start()


def main():
    mode = sys.argv[1]
    if mode == "unix2tcp":
        path, host, port = sys.argv[2], sys.argv[3], int(sys.argv[4])
        if os.path.exists(path):
            os.unlink(path)
        os.umask(0)
        srv = socket.socket(socket.AF_UNIX)
        srv.bind(path)
        srv.listen(64)
        print("relaying", path, "->", f"{host}:{port}", flush=True)
        serve(srv, lambda: socket.create_connection((host, port)))
    elif mode == "tcp2unix":
        port, path = int(sys.argv[2]), sys.argv[3]
        srv = socket.socket()
        srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        srv.bind(("0.0.0.0", port))
        srv.listen(64)
        print("relaying", f"0.0.0.0:{port}", "->", path, flush=True)

        def connect():
            u = socket.socket(socket.AF_UNIX)
            u.connect(path)
            return u

        serve(srv, connect)
    else:
        sys.exit(__doc__)


if __name__ == "__main__":
    main()
