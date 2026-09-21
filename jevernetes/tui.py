"""Optional curses log browser; collection and judgments use the existing engine."""
import copy
import os
import sys
import threading
import unicodedata

from .report import safe_console
from .grouping import group_id
from .search import SearchCache, SearchRun

TABS = ('Important', 'Routine', 'Needs Review', 'All', 'Search')


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
        self.selected = [0]*len(TABS)
        self.anchor = [None]*len(TABS)
        self.detail = None
        self.detail_offset = 0
        self.detail_max = 0
        self.tabs = []
        self.row_hits = []
        self.rows = []
        self.page_size = 1
        self.events = []
        self.search_cache = SearchCache()
        self.search_run = self.search_worker = None
        self.search_events, self.search_groups = {}, {}
        self.search_instances = None
        self.search_input = None
        self.search_cursor = 0
        self.search_mode, self.search_budget = 'jev', 16
        self.search_message = ''
        self.search_return_tab = 0

    def ask(self, mode='jev'):
        if self.tab != 4:
            self.search_return_tab = self.tab
        self.tab, self.detail, self.search_instances = 4, None, None
        if self.search_worker and self.search_worker.is_alive():
            self.search_message = 'Search is running. Press x to stop before asking again.'
            return
        self.search_input = self.search_run.query if self.search_run else ''
        self.search_cursor, self.search_mode = len(self.search_input), mode
        self.search_message = ''

    def submit_search(self):
        try:
            search = SearchRun(self.events, self.search_input, self.search_mode,
                               self.search_budget, cache=self.search_cache)
        except ValueError as error:
            self.search_message = str(error)
            return
        self.search_events = {e['id']:copy.deepcopy(e) for e in self.events}
        self.search_groups = {}
        for event in self.search_events.values():
            self.search_groups.setdefault(group_id(event), []).append(event)
        self.search_run, self.search_input = search, None
        self.search_instances, self.detail = None, None
        self.selected[4], self.anchor[4] = 0, None
        self.search_message = ''
        self.search_worker = threading.Thread(target=search.run, daemon=True)
        self.search_worker.start()

    def stop_search(self):
        if self.search_run:
            self.search_run.stop()

    def search_key(self, key, curses):
        if key == 27:
            self.search_input = None
            if not self.search_run:
                self.tab = self.search_return_tab
        elif key in (10, 13, curses.KEY_ENTER):
            self.submit_search()
        elif key in (curses.KEY_BACKSPACE, 127, 8):
            if self.search_cursor:
                self.search_input = self.search_input[:self.search_cursor-1]+self.search_input[self.search_cursor:]
                self.search_cursor -= 1
        elif key == curses.KEY_DC:
            self.search_input = self.search_input[:self.search_cursor]+self.search_input[self.search_cursor+1:]
        elif key == curses.KEY_LEFT:
            self.search_cursor = max(0, self.search_cursor-1)
        elif key == curses.KEY_RIGHT:
            self.search_cursor = min(len(self.search_input), self.search_cursor+1)
        elif key in (curses.KEY_HOME, 1):
            self.search_cursor = 0
        elif key in (curses.KEY_END, 5):
            self.search_cursor = len(self.search_input)
        elif key == 21:  # Ctrl+U clears the question.
            self.search_input, self.search_cursor = '', 0
        elif key == 9:
            modes = ('jev', 'literal')
            self.search_mode = modes[(modes.index(self.search_mode)+1)%3]
        elif key == 2:  # Ctrl+B cycles the same budgets as the dashboard.
            budgets = (4,16,64)
            self.search_budget = budgets[(budgets.index(self.search_budget)+1)%3]
        elif isinstance(key, str) and key.isprintable() and len(self.search_input) < 500:
            self.search_input = self.search_input[:self.search_cursor]+key+self.search_input[self.search_cursor:]
            self.search_cursor += 1
        elif isinstance(key, int) and 32 <= key < 127:
            self.search_key(chr(key), curses)

    def search_rows(self, result):
        if self.search_instances is not None:
            return self.search_instances
        return [{**self.search_events[match['event_id']], 'search_match':match}
                for match in result['results'] if match['event_id'] in self.search_events] if result else []

    def switch(self, tab):
        if self.tab != 4 and tab % len(TABS) == 4:
            self.search_return_tab = self.tab
        self.tab = tab % len(TABS)
        self.detail = None
        if self.tab == 4 and not self.search_run:
            self.ask()

    def move(self, amount):
        if self.detail:
            self.detail_offset = max(0, min(self.detail_max, self.detail_offset+amount))
        else:
            self.selected[self.tab] = max(0, min(max(0, len(self.rows)-1), self.selected[self.tab]+amount))
            if self.rows:
                self.anchor[self.tab] = self.rows[self.selected[self.tab]]['id']

    def key(self, key, curses, mouse=None):
        char = key if isinstance(key, str) else None
        key = ord(key) if char else key
        if self.search_input is not None and key != 3:
            if key == curses.KEY_MOUSE and mouse:
                _, x, y, _, buttons = mouse
                if buttons & (curses.BUTTON1_CLICKED | curses.BUTTON1_PRESSED):
                    for index, row, left, right, _ in self.tabs:
                        if row == y and left <= x < right and index != 4:
                            self.search_input = None
                            self.switch(index)
                            return True
            self.search_key(char if char and char.isprintable() else key, curses)
            return True
        if key in (ord('q'), ord('Q'), 3):
            return False
        if key == 27:
            if self.detail:
                self.detail = None
            elif self.tab == 4:
                if self.search_instances is not None:
                    self.search_instances = None
                    self.selected[4], self.anchor[4] = 0, None
                else:
                    self.switch(self.search_return_tab)
        elif key in (ord('/'), ord('f'), ord('?')):
            self.ask({ord('/'):'jev',ord('f'):'literal',ord('?'):'jev'}[key])
        elif key == ord('x') and self.tab == 4:
            self.stop_search()
        elif key == ord('i') and self.tab == 4 and self.search_instances is None and self.rows and not self.detail:
            event = self.rows[self.selected[4]]
            match = event.get('search_match')
            if match:
                self.search_instances = [{**e,'search_match':match} for e in self.search_groups.get(group_id(event), [])]
                self.selected[4], self.anchor[4] = 0, None
        elif ord('1') <= key <= ord('5'):
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
        self.events = events
        result = self.search_run.snapshot(limit=100000) if self.search_run else None
        counts = [len(filtered(events, index)) for index in range(4)]
        counts.append(result['result_count'] if result else 0)
        live, usage = report.get('live', {}), report.get('usage', {})
        status = 'stopping' if closing else live.get('status', 'snapshot')
        put(0, 0, f'jevernetes | {status} | {report.get("mode", "")} | {len(events)} retained events', curses.A_BOLD)
        put(1, 0, f'${usage.get("estimated_cost_usd", 0):.6f} estimated | queue {live.get("queue_depth", 0)} | dropped {live.get("dropped", 0)} | gaps {report.get("summary", {}).get("coverage_gaps", 0)}')
        self.tabs, start = tab_layout(counts, width-1)
        for index, y, left, _, text in self.tabs:
            put(y, left, text, curses.A_REVERSE if index == self.tab else curses.A_BOLD)
        if self.search_input is not None:
            put(start, 0, 'Ask about all collected logs, including Routine and pending.', curses.A_BOLD)
            put(start+1, 0, f'Mode: {self.search_mode} | {self.search_budget*8} new groups max | {len(events)} instances')
            put(start+3, 0, '> '+''.join(wrap_cells(self.search_input[:self.search_cursor], width-5)[-1:])+'|'+self.search_input[self.search_cursor:])
            put(start+5, 0, 'Jev semantic search is the default, including IP questions.')
            put(start+6, 0, 'Literal: local only. Jev receives redacted question + logs.')
            put(start+7, 0, 'Jev: $0.01 estimated stop; in-flight requests can exceed it.')
            put(height-2, 0, self.search_message)
            put(height-1, 0, 'Enter search | Tab mode | Ctrl+B budget | Ctrl+U clear | Esc cancel')
            screen.refresh()
            return
        if self.tab == 4:
            if result:
                put(start, 0, f'Search [{result["mode"]}]: {result["query"]}', curses.A_BOLD)
                put(start+1, 0, f'{result["status"]} | {result["matched_groups"]} matches + {result["possible_groups"]} possible | {result["examined_groups"]}/{result["total_groups"]} groups evaluated')
                search_usage = result['usage']
                metering = '' if search_usage['cost_complete'] else ' (incomplete)'
                put(start+2, 0, f'{search_usage["request_attempts"]} requests | {result["cache_hits"]} cached | ~${search_usage["estimated_cost_usd"]:.6f}{metering} | {result["elapsed_seconds"]}s')
                put(start+3, 0, (f'PARTIAL: {result["unexamined_groups"]} unexamined, {result["failed_groups"]} failed. ' if result['partial'] else '') + result['message'])
                start += 5
            self.rows = self.search_rows(result)
        else:
            self.rows = filtered(events, self.tab)
        if height < start+5:
            put(height-2, 0, 'Enlarge terminal to show events.')
            put(height-1, 0, 'q: stop and exit')
            screen.refresh()
            return
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
            if event.get('search_match'):
                match = event['search_match']
                confidence = f'{match["confidence"]:.0%} relevance confidence' if match.get('confidence') is not None else 'local exact' if match['relevance']=='match' else 'not evaluated'
                lines[2:2] = wrap_cells(f'Search: {match["relevance"]} | {confidence} | {match["count"]} grouped instances',width-2)
                if match.get('error'):
                    lines[2:2] = wrap_cells(match['error'],width-2)
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
                if self.tab == 4 and event.get('search_match'):
                    match = event['search_match']
                    label = {'unknown':'NOT EVALUATED','possible':'POSSIBLE','match':'MATCH'}[match['relevance']]
                    score = f' | {match["confidence"]:.0%} relevance' if match.get('confidence') is not None else ' | local exact' if match['relevance']=='match' else ''
                    score += f' | {match["count"]} instances'
                put(y, 0, f'{event.get("timestamp") or "-"} | {label}{score} | {name}', curses.A_REVERSE if index == self.selected[self.tab] else curses.A_BOLD)
                put(y+1, 2, event.get('text', '').split('\n', 1)[0])
                self.row_hits.append((y, index))
            if not self.rows:
                put(start, 0, 'No matching evidence returned so far.' if self.tab==4 else f'No {TABS[self.tab].lower()} events in the retained window.')
            unit = 'groups' if self.tab==4 and self.search_instances is None else 'instances'
            put(height-3, 0, f'{TABS[self.tab]}: {len(self.rows)} {unit} | row {self.selected[self.tab]+1 if self.rows else 0}')
        put(height-2, 0, (self.search_message or 'Search covers the frozen window; / asks again on current logs.') if self.tab==4 else live.get('message') or 'Snapshot complete; inspect events or press q to exit.')
        footer = '/ ask | f local | i instances | x stop | Enter detail | Esc back | q quit' if self.tab==4 else 'Tabs / 1-5 | ↑↓ scroll | / ask | f find | Enter detail | Esc back | q quit'
        put(height-1, 0, 'Stopping collection and search...' if closing else footer)
        screen.refresh()


def browse(args, report=None, session=None):
    curses = validate_terminal(args)
    current = report or session.snapshot()
    lock, finished = threading.Lock(), threading.Event()
    errors, worker = [], None
    browser = Browser()
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
        closing = False
        try:
            while True:
                with lock:
                    snapshot = current
                if errors:
                    break
                browser.draw(screen, snapshot, curses, closing)
                if closing and (not session or finished.is_set()) and (not browser.search_worker or not browser.search_worker.is_alive()):
                    break
                try:
                    key = screen.get_wch()
                except curses.error:
                    key = -1  # No key before the redraw timeout.
                except KeyboardInterrupt:
                    key = 3
                mouse = None
                if key == curses.KEY_MOUSE:
                    try:
                        mouse = curses.getmouse()
                    except curses.error:
                        pass
                if not closing and not browser.key(key, curses, mouse):
                    closing = True
                    browser.stop_search()
                    if session:
                        session.stop()
        finally:
            browser.stop_search()
            if session:
                session.stop()
    try:
        curses.wrapper(screen_loop)
    except curses.error:
        raise ValueError('Cannot initialize the terminal UI; check TERM or omit --tui') from None
    finally:
        browser.stop_search()
        if browser.search_worker:
            browser.search_worker.join()
        if worker:
            session.stop()
            worker.join()
    if errors:
        raise errors[0]
    return current
