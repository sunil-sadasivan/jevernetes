"""Read-only kubectl collection with per-container coverage accounting."""
import concurrent.futures
import json
import subprocess
import tempfile


def kubectl_error(stderr, timed_out=False):
    """Return actionable categories without exposing exec-plugin output/secrets."""
    text = stderr.decode("utf-8", "replace").lower()
    if any(s in text for s in ("no such host", "name resolution", "could not resolve", "server misbehaving")):
        return "Kubernetes API DNS resolution failed; check network/DNS or the cluster endpoint"
    if any(s in text for s in ("x509:", "certificate", "tls handshake")):
        return "Kubernetes API TLS verification failed; check kubeconfig CA and endpoint"
    if any(s in text for s in ("forbidden", "cannot list resource", "cannot get resource")):
        return "Kubernetes access forbidden; the current identity needs permission to list pods and read pod logs"
    if any(s in text for s in ("unauthorized", "must be logged in", "getting credentials", "exec: executable", "exec plugin")):
        return "Kubernetes authentication failed; refresh credentials or check the kubeconfig credential plugin"
    if any(s in text for s in ("connection refused", "no route to host", "network is unreachable")):
        return "Kubernetes API is unreachable; check the cluster endpoint, VPN and control-plane firewall allowlist"
    if timed_out or any(s in text for s in ("timed out", "timeout", "deadline exceeded")):
        return "Kubernetes API connection timed out; check the network, VPN and control-plane firewall allowlist (credential lookup also shares this timeout)"
    return "kubectl failed; check cluster access/RBAC and log availability"


def kubectl(args, timeout=35, max_bytes=32 * 1024 * 1024):
    # File-backed output avoids unbounded subprocess PIPE allocations.
    with tempfile.TemporaryFile() as output, tempfile.TemporaryFile() as errors:
        try:
            request_timeout = max(1, min(30, int(timeout) - 2))
            result = subprocess.run(["kubectl", f"--request-timeout={request_timeout}s", *args],
                                    stdout=output, stderr=errors, timeout=timeout, check=False)
        except subprocess.TimeoutExpired:
            errors.seek(0)
            raise ValueError(kubectl_error(errors.read(16384), timed_out=True)) from None
        except FileNotFoundError:
            raise ValueError("kubectl is not installed") from None
        if result.returncode:
            errors.seek(0)
            raise ValueError(kubectl_error(errors.read(16384)))
        output.seek(0)
        data = output.read(max_bytes + 1)
        if len(data) > max_bytes:
            raise ValueError("kubectl output exceeds configured byte limit")
        return data


def targets(pods, previous=True):
    result = []
    for pod in pods:
        meta, spec, status = pod["metadata"], pod["spec"], pod.get("status", {})
        statuses = {s["name"]: s for key in ("containerStatuses", "initContainerStatuses", "ephemeralContainerStatuses") for s in status.get(key, [])}
        for kind, key in [("container", "containers"), ("init", "initContainers"), ("ephemeral", "ephemeralContainers")]:
            for container in spec.get(key, []):
                name = container["name"]
                base = {"namespace": meta["namespace"], "pod": meta["name"], "container": name, "kind": kind}
                if meta.get("uid"):
                    base["pod_uid"] = meta["uid"]
                if name in statuses:
                    base["restart_count"] = statuses[name].get("restartCount", 0)
                result.append({**base, "previous": False})
                if previous and statuses.get(name, {}).get("restartCount", 0) > 0:
                    result.append({**base, "previous": True, "restart_count": statuses[name]["restartCount"] - 1})
    return sorted(result, key=lambda t: (t["namespace"], t["pod"], t["container"], t["previous"]))


def collect(args, on_stream, run=kubectl):
    context = args.context
    if not context:
        context = run(["config", "current-context"]).decode().strip()
    if not context or context.startswith("-"):
        raise ValueError("A valid Kubernetes context is required")
    prefix = ["--context", context]
    scope = ["--namespace", args.namespace] if args.namespace else ["--all-namespaces"]
    listing = [*prefix, "get", "pods", *scope, "-o", "json"]
    if args.selector:
        listing += ["--selector", args.selector]
    try:
        pods = json.loads(run(listing))["items"]
    except (ValueError, KeyError, TypeError) as error:
        raise ValueError(f"Cannot inventory Kubernetes pods: {error}") from None
    streams = targets(pods, not args.no_previous)

    def fetch(target):
        command = [*prefix, "logs", target["pod"], "--namespace", target["namespace"],
                   "--container", target["container"], "--timestamps=true", f"--since={args.since}",
                   f"--tail={args.tail}", f"--limit-bytes={args.max_bytes}"]
        if target["previous"]:
            command.append("--previous=true")
        source = {"type": "kubernetes", "context": context, **target}
        try:
            data = run(command, max_bytes=args.max_bytes)
            return source, data, None
        except ValueError as error:
            return source, b"", str(error)

    # Process in bounded windows, with deterministic report order.
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.collect_workers) as pool:
        for start in range(0, len(streams), args.collect_workers):
            for source, data, error in pool.map(fetch, streams[start:start + args.collect_workers]):
                on_stream(source, data, error)
    return {"context": context, "namespace": args.namespace or "*", "selector": args.selector,
            "pods": len(pods), "streams": len(streams), "since": args.since, "tail": args.tail,
            "max_bytes_per_stream": args.max_bytes, "previous": not args.no_previous}
