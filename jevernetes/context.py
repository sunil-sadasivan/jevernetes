"""Read-only, bounded context lookup for an event already present in a report."""
import datetime
import io
import json
import re

from .events import parse_stream
from .kubernetes import kubectl

MAX_BYTES = 1024 * 1024
MAX_EVENTS = 2000


def event_time(value):
    try:
        result = datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))
        return result if result.tzinfo else None
    except (ValueError, TypeError, AttributeError):
        return None


def fetch_context(event, seconds=120, run=kubectl):
    if type(seconds) is not int or seconds not in (30, 120, 300, 900):
        raise ValueError('Choose a context window of 30, 120, 300 or 900 seconds')
    source = event.get('source', {})
    stamp = event_time(event.get('timestamp'))
    if source.get('type') != 'kubernetes' or stamp is None:
        raise ValueError('Kubernetes context requires a timestamped Kubernetes event')
    context = source.get('context')
    if not isinstance(context, str) or not context or context.startswith('-') or any(ord(c) < 32 for c in context):
        raise ValueError('The recorded Kubernetes context is invalid')
    for key in ('namespace', 'pod', 'container'):
        if not isinstance(source.get(key), str) or not re.fullmatch(r'[a-z0-9][a-z0-9.-]{0,252}', source[key]):
            raise ValueError('The recorded Kubernetes source is invalid')
    prefix = ['--context', context, '--namespace', source['namespace']]

    def identity():
        try:
            pod = json.loads(run([*prefix, 'get', 'pod', source['pod'], '-o', 'json'], timeout=8, max_bytes=MAX_BYTES))
            statuses = [s for key in ('containerStatuses', 'initContainerStatuses', 'ephemeralContainerStatuses')
                        for s in pod.get('status', {}).get(key, [])]
            status = next(s for s in statuses if s['name'] == source['container'])
            return pod['metadata']['uid'], status.get('restartCount', 0)
        except (KeyError, TypeError, StopIteration, json.JSONDecodeError):
            raise ValueError('The recorded container is no longer available') from None

    uid, restarts = identity()
    if source.get('pod_uid') and source['pod_uid'] != uid:
        raise ValueError('This pod was replaced; Kubernetes no longer has logs for the recorded pod instance')
    previous = bool(source.get('previous'))
    warnings = []
    if 'restart_count' in source:
        if restarts == source['restart_count']:
            previous = False
        elif restarts == source['restart_count'] + 1:
            previous = True
        else:
            raise ValueError('The recorded container instance is no longer retained by Kubernetes')
    else:
        warnings.append('This older report has no container restart identity; the fetched instance cannot be verified.')
    if not source.get('pod_uid'):
        warnings.append('This older report has no pod UID; a replacement pod cannot be distinguished.')
    start, end = stamp - datetime.timedelta(seconds=seconds), stamp + datetime.timedelta(seconds=seconds)
    command = [*prefix, 'logs', source['pod'], '--container', source['container'], '--timestamps=true',
               '--since-time=' + start.isoformat().replace('+00:00', 'Z'), '--tail=-1', f'--limit-bytes={MAX_BYTES}']
    if previous:
        command.append('--previous=true')
    raw = run(command, timeout=12, max_bytes=MAX_BYTES)
    if identity() != (uid, restarts):
        raise ValueError('The container changed while fetching context; retry to verify its instance')
    events, parse_warnings = parse_stream(io.BytesIO(raw), source, MAX_EVENTS)
    warnings.extend(parse_warnings)
    if len(raw) >= MAX_BYTES:
        warnings.append('The 1 MiB read limit was reached; some surrounding logs may be missing.')
    events = [e for e in events if (t := event_time(e.get('timestamp'))) is not None and start <= t <= end]
    if len(events) > 500:
        events = sorted(events, key=lambda e: abs((event_time(e['timestamp']) - stamp).total_seconds()))[:500]
        warnings.append('Showing the nearest 500 events; narrow the time window for more detail.')
    events.sort(key=lambda e: (event_time(e['timestamp']), e['line_start']))
    for e in events:
        e['importance'] = 'unclassified'
        e['context_only'] = True
    if not events:
        warnings.append('No retained logs were returned in this window. Logs may have rotated or the workload may have been quiet.')
    warnings.append('Kubernetes only returns retained logs. Future lines and rotated logs may be unavailable.')
    return {'events': events, 'warnings': warnings, 'source': source,
            'start': start.isoformat(), 'end': end.isoformat(), 'previous': previous}
