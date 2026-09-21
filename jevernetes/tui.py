"""Optional curses log browser; collection and judgments use the existing engine."""
import copy
import os
import sys
import threading
import unicodedata

from .report import safe_console

TABS = ('Important', 'Routine', 'Needs Review', 'All')


def validate_terminal(args):
    if args.json:
        raise ValueError('--tui cannot be combined with --json')
    if not sys.stdin.isatty() or not sys.stdout.isatty() or os.environ.get('TERM', '') in ('', 'dumb'):
        raise ValueError('--tui requires an interactive terminal; omit it for plain or piped output')
    try:
        import curses
    except ImportError:
        raise ValueError('--tui requires Python curses support; use plain output or the dashboard') from None
    return curses


def collected(report):
    events = {e['id']: e for e in report.get('tail_events', [])}
    events.update({e['id']: e for e in report.get('events', [])})
    return list(events.values())


def filtered(events, tab):
    statuses = ({'important'}, {'routine'}, {'uncertain', 'unknown', 'dropped'}, None)[tab]
    return [e for e in events if statuses is None or e.get('importance') in statuses]


def fit(text, width):
    result, used = [], 0
    for char in safe_console(str(text)):
        size = 0 if unicodedata.combining(char) else 2 if unicodedata.east_asian_width(char) in ('W', 'F') else 1
        if used + size > width:
            break
        result.append(char)
        used += size
    return ''.join(result)


def tab_layout(counts, width):
    tabs, y, x = [], 2, 0
    for index, label in enumerate(TABS):
        text = f' {index+1} {label} ({counts[index]}) '
        if x and x + len(text) > width:
            y, x = y+1, 0
        shown = fit(text, width-x)
        tabs.append((index, y, x, x+len(shown), shown))
        x += len(shown)+1
    return tabs, y+2


def wrap_cells(text, width):
    lines, line, used = [], [], 0
    for char in safe_console(text):
        size = 0 if unicodedata.combining(char) else 2 if unicodedata.east_asian_width(char) in ('W', 'F') else 1
        if line and used+size > width:
            lines.append(''.join(line))
            line, used = [], 0
        line.append(char)
        used += size
    return lines+[''.join(line)]


class Browser:
    def __init__(self):
        self.tab = 0
        self.selected = [0]*4
        self.anchor = [None]*4
        self.detail = None
        self.detail_offset = 0
        self.detail_max = 0
        self.tabs = []
        self.row_hits = []
        self.rows = []
        self.page_size = 1

    def switch(self, tab):
        self.tab = tab % len(TABS)
        self.detail = None

    def move(self, amount):
        if self.detail:
            self.detail_offset = max(0, min(self.detail_max, self.detail_offset+amount))
        else:
            self.selected[self.tab] = max(0, min(max(0, len(self.rows)-1), self.selected[self.tab]+amount))
            if self.rows:
                self.anchor[self.tab] = self.rows[self.selected[self.tab]]['id']

    def key(self, key, curses, mouse=None):
        if key in (ord('q'), ord('Q'), 3):
            return False
        if key == 27:
            self.detail = None
        elif ord('1') <= key <= ord('4'):
            self.switch(key-ord('1'))
        elif key in (9, curses.KEY_RIGHT):
            self.switch(self.tab+1)
        elif key in (curses.KEY_BTAB, curses.KEY_LEFT):
            self.switch(self.tab-1)
        elif key in (curses.KEY_DOWN, ord('j')):
            self.move(1)
        elif key in (curses.KEY_UP, ord('k')):
            self.move(-1)
        elif key == curses.KEY_NPAGE:
            self.move(self.page_size)
        elif key == curses.KEY_PPAGE:
            self.move(-self.page_size)
        elif key == curses.KEY_HOME:
            self.move(-1000000)
        elif key == curses.KEY_END:
            self.move(1000000)
        elif key in (10, 13, curses.KEY_ENTER) and self.rows and not self.detail:
            self.detail = copy.deepcopy(self.rows[self.selected[self.tab]])
            self.detail_offset = 0
        elif key == curses.KEY_MOUSE and mouse:
            _, x, y, _, buttons = mouse
            if buttons & curses.BUTTON4_PRESSED:
                self.move(-3)
            elif buttons & getattr(curses, 'BUTTON5_PRESSED', 0):
                self.move(3)
            elif buttons & (curses.BUTTON1_CLICKED | curses.BUTTON1_PRESSED):
                for index, row, left, right, _ in self.tabs:
                    if row == y and left <= x < right:
                        self.switch(index)
                        return True
                if not self.detail:
                    for row, index in self.row_hits:
                        if y in (row, row+1):
                            self.selected[self.tab] = index
                            self.anchor[self.tab] = self.rows[index]['id']
        return True

    def draw(self, screen, report, curses, closing=False):
        height, width = screen.getmaxyx()
        screen.erase()
        def put(y, x, text, style=0):
            if 0 <= y < height and x < width-1:
                try:
                    screen.addstr(y, x, fit(text, width-x-1), style)
                except curses.error:
                    pass  # A resize may race a repaint, including a wide glyph.
        self.tabs, self.row_hits = [], []
        if width < 35 or height < 10:
            put(0, 0, 'Enlarge terminal (35x10 minimum).')
            put(1, 0, 'q: stop and exit')
            screen.refresh()
            return
        events = collected(report)
        counts = [len(filtered(events, index)) for index in range(4)]
        live, usage = report.get('live', {}), report.get('usage', {})
        status = 'stopping' if closing else live.get('status', 'snapshot')
        put(0, 0, f'jevernetes | {status} | {report.get("mode", "")} | {len(events)} retained events', curses.A_BOLD)
        put(1, 0, f'${usage.get("estimated_cost_usd", 0):.6f} estimated | queue {live.get("queue_depth", 0)} | dropped {live.get("dropped", 0)} | gaps {report.get("summary", {}).get("coverage_gaps", 0)}')
        self.tabs, start = tab_layout(counts, width-1)
        for index, y, left, _, text in self.tabs:
            put(y, left, text, curses.A_REVERSE if index == self.tab else curses.A_BOLD)
        if height < start+5:
            put(height-2, 0, 'Enlarge terminal to show events.')
            put(height-1, 0, 'q: stop and exit')
            screen.refresh()
            return
        self.rows = filtered(events, self.tab)
        anchor = self.anchor[self.tab]
        index = next((i for i, e in enumerate(self.rows) if e['id'] == anchor), self.selected[self.tab])
        self.selected[self.tab] = min(index, max(0, len(self.rows)-1))
        self.page_size = max(1, (height-start-3)//2)
        if self.detail:
            event = self.detail
            source = event.get('source', {})
            name = source.get('path') or '/'.join(source.get(k, '') for k in ('namespace', 'pod', 'container'))
            lines = [f'{event.get("timestamp") or "No timestamp"} | {name}',
                     f'{event.get("importance", "unknown")} | lines {event.get("line_start")}–{event.get("line_end")}', '']
            for line in event.get('text', '').splitlines():
                lines.extend(wrap_cells(line, width-2))
            self.detail_max = max(0, len(lines)-(height-start-2))
            self.detail_offset = min(self.detail_offset, self.detail_max)
            for y, text in enumerate(lines[self.detail_offset:height-start-2+self.detail_offset], start):
                put(y, 0, text)
        else:
            first = (self.selected[self.tab]//self.page_size)*self.page_size
            for index, event in enumerate(self.rows[first:first+self.page_size], first):
                y = start+(index-first)*2
                source = event.get('source', {})
                name = source.get('path') or '/'.join(source.get(k, '') for k in ('namespace', 'pod', 'container'))
                confidence = event.get('importance_confidence')
                label = event.get('review', {}).get('status') or event.get('importance', 'unknown')
                score = f' | {confidence:.0%} confidence' if isinstance(confidence, (int, float)) and not isinstance(confidence, bool) else ''
                put(y, 0, f'{event.get("timestamp") or "-"} | {label}{score} | {name}', curses.A_REVERSE if index == self.selected[self.tab] else curses.A_BOLD)
                put(y+1, 2, event.get('text', '').split('\n', 1)[0])
                self.row_hits.append((y, index))
            if not self.rows:
                put(start, 0, f'No {TABS[self.tab].lower()} events in the retained window.')
            put(height-3, 0, f'{TABS[self.tab]}: {len(self.rows)} instances | row {self.selected[self.tab]+1 if self.rows else 0} | counts cover retained events')
        put(height-2, 0, live.get('message') or 'Snapshot complete; inspect events or press q to exit.')
        put(height-1, 0, 'Stopping collection...' if closing else 'Click tabs / 1-4 / Tab | ↑↓ scroll | Enter detail | Esc back | q quit')
        screen.refresh()


def browse(args, report=None, session=None):
    curses = validate_terminal(args)
    current = report or session.snapshot()
    lock, finished = threading.Lock(), threading.Event()
    errors, worker = [], None
    def publish(value):
        nonlocal current
        with lock:
            current = copy.deepcopy(value)
    def collect():
        try:
            publish(session.run(publish))
        except Exception as error:
            errors.append(error)
        finally:
            finished.set()
    def screen_loop(screen):
        nonlocal worker
        screen.keypad(True)
        screen.timeout(100)
        try:
            curses.curs_set(0)
        except curses.error:
            pass
        curses.mouseinterval(0)
        try:
            curses.mousemask(curses.ALL_MOUSE_EVENTS)
        except curses.error:
            pass  # Keyboard navigation remains available without mouse support.
        if session:
            worker = threading.Thread(target=collect, daemon=True)
            worker.start()
        browser, closing = Browser(), False
        try:
            while True:
                with lock:
                    snapshot = current
                if errors:
                    break
                browser.draw(screen, snapshot, curses, closing)
                if closing and (not session or finished.is_set()):
                    break
                try:
                    key = screen.getch()
                except KeyboardInterrupt:
                    key = ord('q')
                mouse = None
                if key == curses.KEY_MOUSE:
                    try:
                        mouse = curses.getmouse()
                    except curses.error:
                        pass
                if not closing and not browser.key(key, curses, mouse):
                    closing = True
                    if session:
                        session.stop()
        finally:
            if session:
                session.stop()
    try:
        curses.wrapper(screen_loop)
    except curses.error:
        raise ValueError('Cannot initialize the terminal UI; check TERM or omit --tui') from None
    finally:
        if worker:
            session.stop()
            worker.join()
    if errors:
        raise errors[0]
    return current
