import copy
import curses
import io
import threading
import unittest
from unittest.mock import patch

from jevernetes.cli import parser, run
from jevernetes.events import parse_stream
from jevernetes.report import build_report
from jevernetes.tui import Browser, browse, collected, filtered, fit, validate_terminal, wrap_cells


def fixture():
    events = parse_stream(io.BytesIO(b'ERROR failure\nINFO ready\nWARN retry\nINFO unclassified\n'), {'type':'file','path':'synthetic.log'}, 10)[0]
    for event, importance in zip(events, ('important','routine','uncertain','unknown')):
        event.update(importance=importance, severity='unknown', category='unknown')
    return build_report(events, [], {}, 'offline-rules', 0, 0)


class Screen:
    def __init__(self, height=24, width=100, keys=()):
        self.height, self.width, self.keys = height, width, iter(keys)
        self.lines = {}
    def getmaxyx(self): return self.height, self.width
    def erase(self): self.lines = {}
    def addstr(self, y, x, text, style=0):
        self.lines[(y,x)] = text
        if y >= self.height or x >= self.width:
            raise AssertionError('Out of bounds')
    def refresh(self): pass
    def keypad(self, value): pass
    def timeout(self, value): pass
    def getch(self): return next(self.keys, ord('q'))


class TuiTests(unittest.TestCase):
    def test_filters_include_uncertain_unknown_and_dropped(self):
        report = fixture()
        report['tail_events'] = [dict(report['events'][0],importance='pending'),
                                 dict(report['events'][0],id='pending',importance='pending'),
                                 dict(report['events'][0],id='dropped',importance='dropped')]
        events = collected(report)
        self.assertEqual(len(events),6)
        self.assertEqual([len(filtered(events,i)) for i in range(4)],[1,1,3,6])
        self.assertEqual(events[0]['importance'],'important')

    def test_keyboard_mouse_tabs_wrapping_and_row_selection(self):
        browser, screen, report = Browser(), Screen(width=45), fixture()
        browser.draw(screen, report, curses)
        self.assertGreater(len({tab[1] for tab in browser.tabs}),1)
        for index, y, left, right, _ in browser.tabs[:]:
            self.assertTrue(browser.key(curses.KEY_MOUSE,curses,(0,left+1,y,0,curses.BUTTON1_PRESSED)))
            self.assertEqual(browser.tab,index)
        browser.key(ord('1'),curses)
        browser.key(9,curses)
        self.assertEqual(browser.tab,1)
        browser.key(curses.KEY_LEFT,curses)
        self.assertEqual(browser.tab,0)
        browser.key(ord('4'),curses)
        browser.draw(screen,report,curses)
        y,index=browser.row_hits[-1]
        browser.key(curses.KEY_MOUSE,curses,(0,10,y,0,curses.BUTTON1_CLICKED))
        self.assertEqual(browser.selected[3],index)
        browser.key(10,curses)
        browser.draw(screen,report,curses)
        self.assertEqual(browser.detail['id'],report['events'][index]['id'])
        browser.key(27,curses)
        self.assertIsNone(browser.detail)
        self.assertFalse(browser.key(ord('q'),curses))

    def test_scroll_keeps_selection_stable_as_live_events_change(self):
        browser,screen,report=Browser(),Screen(height=12),fixture()
        browser.switch(3)
        browser.draw(screen,report,curses)
        browser.key(curses.KEY_END,curses)
        selected=browser.anchor[3]
        report['events'].pop(0)
        browser.draw(screen,report,curses)
        self.assertEqual(browser.rows[browser.selected[3]]['id'],selected)
        browser.key(curses.KEY_HOME,curses)
        self.assertEqual(browser.selected[3],0)
        browser.key(curses.KEY_NPAGE,curses)
        self.assertGreater(browser.selected[3],0)

    def test_control_sequences_removed_and_small_terminal_safe(self):
        self.assertEqual(fit('\x1b[31mred\x1b[0m\x07',3),'red')
        self.assertEqual(fit('界界',3),'界')
        self.assertEqual(wrap_cells('界界abc',4),['界界','abc'])
        browser,screen=Browser(),Screen(height=3,width=20)
        browser.draw(screen,fixture(),curses)
        self.assertTrue(screen.lines)
        self.assertFalse(browser.tabs)

    def test_detail_preserves_multiline_text_and_freezes_event(self):
        browser,screen,report=Browser(),Screen(),fixture()
        report['events'][0]['text']='ERROR failure\n  at example.py:12\n  nested detail'
        browser.draw(screen,report,curses)
        browser.key(10,curses)
        report['events'][0]['text']='changed'
        browser.draw(screen,report,curses)
        self.assertIn('example.py:12',' '.join(screen.lines.values()))

    def test_rejects_json_and_nonterminal_before_collection(self):
        args=parser().parse_args(['k8s','--live','--offline','--tui','--json'])
        with self.assertRaisesRegex(ValueError,'--json'):
            validate_terminal(args)
        args.json=False
        with patch('sys.stdin.isatty',return_value=False), patch('jevernetes.live.LiveSession') as session:
            with self.assertRaisesRegex(ValueError,'interactive terminal'):
                run(args)
            session.assert_not_called()

    def test_snapshot_and_live_shutdown_restore_terminal_and_return_report(self):
        args=parser().parse_args(['files','--tui','--offline','synthetic.log'])
        report=fixture()
        class Session:
            def __init__(self): self.stopped=threading.Event(); self.completed=False
            def snapshot(self): return copy.deepcopy(report)
            def stop(self): self.stopped.set()
            def run(self,publish):
                publish(self.snapshot())
                self.stopped.wait(2)
                self.completed=True
                return self.snapshot()
        with patch('jevernetes.tui.validate_terminal',return_value=curses), \
             patch('curses.wrapper',side_effect=lambda fn:fn(Screen(keys=[ord('2'),ord('q')]))), \
             patch('curses.curs_set'),patch('curses.mouseinterval'),patch('curses.mousemask'):
            self.assertEqual(browse(args,report=report),report)
            session=Session()
            self.assertEqual(browse(args,session=session),report)
            self.assertTrue(session.stopped.is_set())
            self.assertTrue(session.completed)

    def test_live_failure_stops_collection_and_propagates_error(self):
        args=parser().parse_args(['k8s','--live','--tui','--offline'])
        class Session:
            stopped=False
            def snapshot(self): return fixture()
            def stop(self): self.stopped=True
            def run(self,publish): raise ValueError('synthetic collection failure')
        session=Session()
        with patch('jevernetes.tui.validate_terminal',return_value=curses), \
             patch('curses.wrapper',side_effect=lambda fn:fn(Screen())), \
             patch('curses.curs_set'),patch('curses.mouseinterval'),patch('curses.mousemask'):
            with self.assertRaisesRegex(ValueError,'synthetic collection failure'):
                browse(args,session=session)
        self.assertTrue(session.stopped)


if __name__=='__main__': unittest.main()
