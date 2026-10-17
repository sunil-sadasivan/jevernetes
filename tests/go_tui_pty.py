"""Exercise the real Go binary in a PTY using synthetic logs; no credentials or network."""
import errno
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import time


def check(binary, action):
    with tempfile.TemporaryDirectory(prefix="jevernetes-tui-") as directory:
        root = Path(directory)
        fixture = root / "synthetic.log"
        fixture.write_text("ERROR database unavailable\n  at synthetic.rs:12\nINFO ready\n")
        report = root / "report.json"
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 140, 0, 0))
        original = termios.tcgetattr(slave)
        env = {k: v for k, v in os.environ.items() if not k.startswith(("TYPESAFE", "KUBECONFIG"))}
        env["TERM"] = "xterm-256color"
        args = [str(binary), "files", str(fixture), "--offline", "--tui", "--output", str(report)]
        if action == "failure":
            config = root / "empty-kubeconfig"
            config.write_text('apiVersion: v1\nkind: Config\ncontexts: []\nclusters: []\nusers: []\ncurrent-context: ""\n')
            config.chmod(0o600)
            env["KUBECONFIG"] = str(config)
            args = [str(binary), "k8s", "--context", "missing-fixture", "--offline", "--tui"]
        elif action == "partial":
            args.insert(3, str(root / "missing.log"))
        child = subprocess.Popen(
            args,
            stdin=slave, stdout=slave, stderr=slave, env=env, start_new_session=True,
        )
        captured = bytearray()

        def read():
            if select.select([master], [], [], 0.05)[0]:
                try:
                    captured.extend(os.read(master, 65536))
                except OSError as error:
                    if error.errno != errno.EIO:
                        raise

        def wait_for(text):
            deadline = time.monotonic() + 8
            while text not in captured:
                read()
                if child.poll() is not None or time.monotonic() > deadline:
                    raise AssertionError(f"TUI did not display {text!r}; exit={child.poll()}")

        try:
            wait_for(b"Jevernetes")
            if action != "failure":
                wait_for(b"synthetic.log")
            if action == "failure":
                wait_for(b"Collection failed")
                assert b"Collection complete" not in captured
                assert child.poll() is None, "Failed collection should remain available for inspection"
                os.write(master, b"q")
                expected = 1
            elif action == "partial":
                wait_for("Collection stopped · partial coverage".encode())
                assert b"Collection complete" not in captured
                os.write(master, b"q")
                expected = 2
            elif action == "search":
                wait_for(b"Collection complete")
                wait_for(b"synthetic.log")
                os.write(master, b"f")
                wait_for(b"Exact text")
                os.write(master, b"database\r")
                wait_for(b"Finished searching")
                os.write(master, b"\r")
                wait_for(b"synthetic.rs:12")
                os.write(master, b"q")
                expected = 0
            elif action == "ctrl-c":
                os.write(master, b"f")
                wait_for(b"Exact text")
                os.write(master, b"\x03")
                expected = 130
            else:
                child.send_signal(signal.SIGTERM)
                expected = 130
            deadline = time.monotonic() + 8
            while child.poll() is None:
                read()
                if time.monotonic() > deadline:
                    raise AssertionError("TUI failed to exit")
            read()
            assert child.returncode == expected, child.returncode
            # Settle Darwin PENDIN bookkeeping without consuming input (as in exec-auth PTY tests).
            select.select([slave], [], [], 0)
            assert termios.tcgetattr(slave) == original, f"Terminal attributes not restored: {original!r} => {termios.tcgetattr(slave)!r}"
            for sequence in (b"\x1b[?1049l", b"\x1b[?1000l", b"\x1b[?25h"):
                assert sequence in captured, f"Missing terminal cleanup: {sequence!r}"
            if action == "failure":
                assert not report.exists()
                print("PASS: startup failure visible before quit; exit 1; terminal restored")
                return
            value = json.loads(report.read_text())
            assert value["runtime"] == "go"
            assert value["summary"]["events"] == 2
            assert value["summary"]["unknown"] == 0
            assert value["usage"]["request_attempts"] == 0
            if action == "partial":
                assert value["summary"]["complete_within_window"] is False
            assert report.stat().st_mode & 0o777 == 0o600
            print(f"PASS: {action}; terminal restored, private report saved")
        finally:
            if child.poll() is None:
                child.kill()
            child.wait(timeout=5)
            os.close(master)
            os.close(slave)


if __name__ == "__main__":
    binary = Path(sys.argv[1] if len(sys.argv) > 1 else ".runs/jevernetes").resolve()
    for action in ("failure", "partial", "search", "ctrl-c", "sigterm"):
        check(binary, action)
