# Dashboard demo recording

[`images/live-demo.gif`](images/live-demo.gif) shows the compact dashboard: live logs and usage counters, selecting related errors, copying an agent investigation prompt, bulk acknowledgment, and saving expected-event rules, including standalone log fragments.

All cluster names, pod names, timestamps, messages, classifications, and usage values are fixtures. The recording does not use Kubernetes, saved reports, or an API key. No requests are sent to Jev or a coding agent. Copying uses the browser clipboard, and the recorder verifies that it contains the two selected errors and excludes an unselected routine event.

To recreate it, install Node.js 22 or newer, Chrome/Chromium, and `ffmpeg`, then run from the repository root:

```sh
node tools/record-demo.mjs
```

On macOS the recorder uses the standard Google Chrome application path. Elsewhere, or for another Chromium installation, set `CHROME_BIN` to the browser executable:

```sh
CHROME_BIN=/usr/bin/chromium node tools/record-demo.mjs
```

The recorder starts a temporary loopback server serving only the dashboard assets and fixture API responses, uses a fresh browser profile, blocks browser-page requests outside that server, and writes `docs/images/live-demo.gif`. Its caption identifies the simulated data throughout the animation. It closes the browser and server and removes the browser profile afterward. Screenshots and a visible-text transcript remain in a temporary directory printed at completion for visual and privacy review before publishing.

## CLI demo recording

[`images/cli-live-tail.gif`](images/cli-live-tail.gif) is a separate recording of the actual `python3 -m jevernetes k8s -f` command. Five fixed, timestamped synthetic messages arrive two seconds apart. The real collector, redactor, offline classifier, and terminal printer produce the displayed output, including keyword labels, stream status, and a clean timed stop. Offline rules label keyword matches `important` and other messages `uncertain`; no Jev classifications are simulated.

From the repository root, use Python 3.11+ and an existing `ffmpeg` build with `drawtext`, `palettegen`, and `paletteuse`:

```sh
python3 tools/record-cli-demo.py
```

The default font is macOS Menlo. On other systems, set `DEMO_FONT` to an installed monospace TTF/TTC file. No Python packages, browser, or terminal-recording software are needed.

The recorder runs in a fresh temporary working directory with an allowlisted environment: no inherited credentials, an empty home, `KUBECONFIG` pointed at the null device, and a `PATH` containing only Python and a synthetic `kubectl`. That stub accepts exactly the fixture inventory and follow commands for `synthetic-demo`, rejects other arguments, and never calls Kubernetes. Existing review rules and reports are not read. `--offline` prevents provider calls. Do not copy the command out of the GIF to recreate the fixture; run the recorder to get this isolation.

The terminal framing, prompt, line wrapping, and colors are presentation added by the recorder; stdout and stderr are captured together without rewriting their contents. The animation preserves observed arrival timing, with a short opening and closing hold. Fixture content and order are deterministic; thread scheduling, installed font, Python, and ffmpeg versions can change frame timing or encoded bytes.

The script checks redaction, event counts, keyword labels, clean exit, stub calls, and the 5 MiB size limit before replacing the GIF. Its printed temporary directory retains `capture.json` (timed CLI output), `kubectl-calls.jsonl`, `visible-text.json` (every frame's text), and PNG frames for visual/privacy review. These files contain only fixture data; remove the temporary directory after review.

Verify the encoded artifact and inspect representative frames before committing:

```sh
ffprobe -v error -count_frames \
  -show_entries stream=codec_name,width,height,nb_read_frames:format=format_name,duration,size:format_tags \
  -of json docs/images/cli-live-tail.gif
git diff --check
```
