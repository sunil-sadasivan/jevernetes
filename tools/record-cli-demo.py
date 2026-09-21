"""Record the actual CLI against a synthetic kubectl; render with ffmpeg only."""
import json
import os
from pathlib import Path
import selectors
import shutil
import subprocess
import sys
import tempfile
import textwrap
import time


ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / "docs/images/cli-live-tail.gif"
COMMAND = [
    "python3", "-m", "jevernetes", "k8s", "-f", "--tail", "0", "--offline",
    "--context", "synthetic-demo", "--namespace", "demo",
    "--duration", "11", "--batch-size", "1",
]
PROMPT = [
    "$ " + " ".join(COMMAND[:8]) + " \\",
    "    " + " ".join(COMMAND[8:12]) + " \\",
    "    " + " ".join(COMMAND[12:]),
    "",
]
SAMPLES = [
    "INFO GET /health status=200 duration=3ms",
    "INFO request accepted password=synthetic-placeholder",
    "ERROR database pool exhausted active=20 waiting=48",
    "ERROR GET /orders status=503 duration=2000ms",
    "INFO database pool recovered; queue drained",
]

# Only this executable is available as kubectl. Unexpected commands fail closed.
FAKE_KUBECTL = '''import json
from pathlib import Path
import sys
import time

args = sys.argv[1:]
inventory = ["--request-timeout=10s", "--context", "synthetic-demo", "get", "pods", "-o", "json",
             "--namespace", "demo"]
follow = ["--context", "synthetic-demo", "--request-timeout=0", "logs", "api-demo",
          "--namespace", "demo", "--container", "app", "--follow=true",
          "--timestamps=true", "--pod-running-timeout=10s", "--since=1h", "--tail=0"]
with open("kubectl-calls.jsonl", "a") as audit:
    audit.write(json.dumps(args) + "\\n")
if args == inventory:
    print(json.dumps({"items": [{
        "metadata": {"uid": "synthetic-pod", "namespace": "demo", "name": "api-demo"},
        "status": {"containerStatuses": [{"name": "app", "restartCount": 0,
                                          "state": {"running": {}}}]}
    }]}))
elif args == follow:
    samples = json.loads(Path("samples.json").read_text())
    start = time.monotonic()
    for i, message in enumerate(samples):
        time.sleep(max(0, start + 0.2 + i * 2 - time.monotonic()))
        print(f"2026-01-15T12:00:{i * 2:02d}Z {message}", flush=True)
    time.sleep(30)  # Stay open until the real CLI stops its follow subprocess.
else:
    sys.exit("Synthetic kubectl rejected an unexpected command")
'''


def capture(temp):
    binary = temp / "bin"
    binary.mkdir()
    (binary / "python3").symlink_to(sys.executable)
    fake = binary / "kubectl"
    fake.write_text(f"#!{sys.executable}\n" + FAKE_KUBECTL)
    fake.chmod(0o700)
    (temp / "samples.json").write_text(json.dumps(SAMPLES))
    # No inherited credentials, real kubectl, user config, or saved review rules.
    env = {"PATH": str(binary), "HOME": str(temp), "KUBECONFIG": os.devnull,
           "PYTHONPATH": str(ROOT), "PYTHONUNBUFFERED": "1",
           "PYTHONIOENCODING": "utf-8", "PYTHONDONTWRITEBYTECODE": "1"}
    started = time.monotonic()
    chunks = []
    with subprocess.Popen(COMMAND, cwd=temp, env=env, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, start_new_session=True) as process:
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(process.stdout, selectors.EVENT_READ)
                while True:
                    if time.monotonic() - started > 20:
                        raise RuntimeError("Synthetic CLI exceeded recording timeout")
                    if not selector.select(.2):
                        continue
                    data = os.read(process.stdout.fileno(), 65536)
                    if not data:
                        break
                    chunks.append({"time": round(time.monotonic() - started, 3),
                                   "text": data.decode("utf-8")})
            if process.wait(timeout=3) != 0:
                raise RuntimeError("Synthetic CLI failed; inspect capture.json")
        finally:
            if process.poll() is None:
                # Include any synthetic follow subprocess in timeout cleanup.
                import signal
                os.killpg(process.pid, signal.SIGTERM)
                process.wait(timeout=3)
            (temp / "capture.json").write_text(json.dumps(chunks, indent=2))
    transcript = "".join(chunk["text"] for chunk in chunks)
    assert 'password="[REDACTED]"' in transcript
    assert "synthetic-placeholder" not in transcript
    assert "[important]" in transcript and "[uncertain]" in transcript
    assert "[tail] stopped" in transcript and "dropped 0" in transcript
    for i in range(1, len(SAMPLES) + 1):
        assert transcript.count(f"#{i} ") == 1, "Missing or duplicate log event"
    calls = (temp / "kubectl-calls.jsonl").read_text().splitlines()
    assert len(calls) == 2, "Expected exactly one inventory and one follow"
    return chunks


def render(temp, chunks, ffmpeg, font):
    shutil.copyfile(font, temp / "font.ttf")
    frames = []
    visible = []

    def frame(lines, at):
        rows = []
        for line in lines:
            color = "cbd5e1"
            if line.startswith(("$", "    --")):
                color = "a7f3d0"
            elif line.startswith("[tail]"):
                color = "94a3b8"
            elif "[important]" in line:
                color = "fdba74"
            for part in textwrap.wrap(line, width=96, subsequent_indent="    ",
                                      replace_whitespace=False) or [""]:
                rows.append((part, color))
        assert len(rows) <= 21, "Recording exceeds terminal viewport"
        index = len(frames)
        filters = [
            "drawbox=x=16:y=16:w=1088:h=688:color=0x111827:t=fill",
            "drawbox=x=16:y=16:w=1088:h=60:color=0x1e293b:t=fill",
            "drawbox=x=32:y=36:w=10:h=10:color=0x6ee7b7:t=fill",
        ]
        labels = [("jevernetes  /  CLI live tail", 58, 31, 22, "e2e8f0"),
                  ("SYNTHETIC DATA  |  fake kubectl  |  offline", 32, 94, 18, "a5b4fc"),
                  ("Local keyword labels + redaction  /  no cluster or API access",
                   32, 668, 17, "94a3b8")]
        labels += [(line, 32, 138 + i * 24, 18, color)
                   for i, (line, color) in enumerate(rows)]
        for i, (label, x, y, size, color) in enumerate(labels):
            name = f"text-{index:03}-{i:02}.txt"
            (temp / name).write_text(label)
            filters.append(f"drawtext=fontfile=font.ttf:textfile={name}:expansion=none:"
                           f"fontsize={size}:fontcolor=0x{color}:x={x}:y={y}")
        script = f"frame-{index:03}.filters"
        (temp / script).write_text(",".join(filters))
        name = f"frame-{index:03}.png"
        subprocess.run([ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
                        "-f", "lavfi", "-i", "color=c=0x080e1a:s=1120x720",
                        "-filter_script:v", script, "-frames:v", "1", name],
                       cwd=temp, check=True)
        frames.append((name, at))
        visible.append({"time": at, "lines": [label[0] for label in labels]})

    frame(["$ _"], 0)
    frame(PROMPT, .6)
    text = ""
    for chunk in chunks:
        text += chunk["text"]
        frame(PROMPT + text.splitlines(), 2 + chunk["time"])
    frame(PROMPT + text.splitlines() + ["$ _"], frames[-1][1] + .3)
    entries = []
    for i, (name, at) in enumerate(frames):
        duration = frames[i + 1][1] - at if i + 1 < len(frames) else 3
        entries.append(f"file '{name}'\nduration {max(.1, duration):.3f}\n")
    (temp / "frames.txt").write_text("".join(entries) + f"file '{frames[-1][0]}'\n")
    (temp / "visible-text.json").write_text(json.dumps(visible, indent=2))
    subprocess.run([ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
                    "-f", "concat", "-safe", "0", "-i", "frames.txt",
                    "-filter_complex", "[0:v]fps=10,split[a][b];"
                    "[a]palettegen=stats_mode=full[p];"
                    "[b][p]paletteuse=dither=bayer:bayer_scale=3",
                    "-t", f"{frames[-1][1] + 3:.3f}",
                    "-map_metadata", "-1", "-loop", "0", "demo.gif"], cwd=temp, check=True)
    assert (temp / "demo.gif").stat().st_size < 5 * 1024 * 1024
    OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(temp / "demo.gif", OUTPUT)


def main():
    ffmpeg = shutil.which("ffmpeg")
    font = Path(os.environ.get("DEMO_FONT", "/System/Library/Fonts/Menlo.ttc"))
    if not ffmpeg or not font.is_file():
        sys.exit("Requires ffmpeg with drawtext and a monospace font (set DEMO_FONT).")
    temp = Path(tempfile.mkdtemp(prefix="jevernetes-cli-demo-"))
    print(f"Recording and review files: {temp}", flush=True)
    render(temp, capture(temp), ffmpeg, font)
    print(f"Created {OUTPUT.relative_to(ROOT)} ({OUTPUT.stat().st_size:,} bytes)")


if __name__ == "__main__":
    main()
