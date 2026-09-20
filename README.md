# jevernetes

Live Kubernetes log analysis in your terminal or a local dashboard, powered by [TypeSafe Jev](https://docs.typesafe.ai). Find events worth investigating, inspect their context, and hand selected logs to your coding agent.

![Live Kubernetes dashboard showing streaming logs, selecting two important events, and copying an investigation prompt for a coding agent](docs/images/live-demo.gif)

Live tail → select important events → copy a prompt for Claude, Codex, or another coding agent.

## Quick start

Using Claude, Codex, or another coding agent? Paste this prompt:

```text
Set up https://github.com/sunil-sadasivan/jevernetes locally. Check that Python
3.11+ and kubectl are available, show me my current Kubernetes context, and
launch the local dashboard. Help me start a read-only live tail in offline
mode first, then explain how to enable Jev analysis and set a cost threshold.
Never commit API keys, kubeconfigs, logs, or reports.
```

Or set it up yourself. You need **Python 3.11+**, **kubectl**, and permission to list pods and read logs. No Python runtime dependencies are required.

```sh
git clone https://github.com/sunil-sadasivan/jevernetes.git
cd jevernetes

# Check your cluster access.
kubectl config current-context
kubectl get pods --all-namespaces

# Optional: enable Jev semantic analysis before starting the dashboard.
export TYPESAFE_API_KEY='your-api-key'

python3 -m jevernetes dashboard
```

Open **http://127.0.0.1:8792** and click **Start live tail**. Without an API key, choose **Offline keyword rules**. Keep the terminal running while using the dashboard.

Collection uses your existing kubectl configuration and is read-only. All namespaces are included by default; use `--namespace my-namespace` or `--context my-cluster` to narrow the scope.

## Investigate in the dashboard

- **Live tail:** follow incoming logs, filter by pod or text, and watch token usage and estimated cost.
- **View in context:** inspect surrounding lines and fetch additional retained logs from Kubernetes.
- **Copy investigation prompt:** select rows in Important or Needs review, then copy their evidence into Claude, Codex, or another agent.
- **Acknowledge / Mark as expected:** dismiss a reviewed event or suppress future matches. Inspect and manage rules in **Rules & reviews**.

Dark mode is the default; light mode is one click away. Reports and review rules are saved locally in `.runs/`.

## Terminal usage

```sh
# Follow new logs using local keyword rules, without an API key.
python3 -m jevernetes k8s -f --tail 0 --offline

# Live Jev analysis with an AI batch budget and estimated cost threshold.
python3 -m jevernetes k8s -f --tail 100 --max-batches 5000 --max-cost 0.25

# Analyze a snapshot of the last hour.
python3 -m jevernetes k8s --since 1h --output .runs/cluster.json

# Try a local log file without an API key.
python3 -m jevernetes files examples/mixed.log --offline
```

Jev analysis requires `TYPESAFE_API_KEY`. Cost is an estimate; in-flight requests and missing usage can exceed the stop threshold. Check **Collection coverage** for missing or truncated logs.

See the [usage reference](docs/usage.md) for installation, filters, budgets, troubleshooting, file inputs, and exit codes. Run `python3 -m jevernetes --help` for commands.

## Data and security

Jev mode sends redacted log text and source metadata to TypeSafe. Redaction is best effort; use `--offline` when logs must stay local, and review copied prompts before sharing. The dashboard binds to localhost. Keep logs, reports, kubeconfigs, and API keys out of version control.

[Security policy](SECURITY.md) · [Contributing and CI checks](CONTRIBUTING.md) · [Validation](VALIDATION.md)

Licensed under [Apache 2.0](LICENSE). Inspired by [Log Sentinel](https://github.com/dabit3/jev-experiments/tree/main/log-sentinel).
