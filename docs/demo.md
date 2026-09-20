# Dashboard demo recording

[`images/live-demo.gif`](images/live-demo.gif) shows the real dashboard rendering a synthetic Kubernetes session: incoming logs and usage counters, filtering important events, selecting two related errors, copying an investigation prompt, and previewing its evidence.

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
