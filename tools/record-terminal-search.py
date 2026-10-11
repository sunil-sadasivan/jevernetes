"""Render the actual TUI using synthetic logs/decisions; no cluster or API access.

Requires ffmpeg with drawtext and a monospace font (DEMO_FONT may override).
The Browser renderer and keyboard/mouse handlers are the production code.
"""
import curses
import datetime
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from jevernetes.events import parse_stream
from jevernetes.report import build_report
from jevernetes.tui import Browser


class Screen:
    def __init__(self): self.lines = []
    def getmaxyx(self): return 25, 112
    def erase(self): self.lines = []
    def addstr(self, y, x, text, style=0): self.lines.append((y, x, text, style))
    def refresh(self): pass


def fixture():
    samples = [
        ('ERROR database connection pool exhausted: active=50 waiting=128\n  at db/pool.py:42\n  request_id=demo-42', 24, 'important', .98),
        ('ERROR GET /orders 503: database connection timeout after 10000ms', 12, 'important', .96),
        ('ERROR database connection refused; retries exhausted', 6, 'important', .94),
        ('WARN database query slow: duration=3200ms', 4, 'uncertain', .64),
        ('INFO GET /health status=200 duration=3ms', 8, 'routine', .98),
        ('INFO GET /orders status=200 client=192.0.2.42', 6, 'routine', .97),
        ('INFO GET /catalog status=200 client=192.0.2.42', 4, 'routine', .97),
        ('INFO request accepted client=192.0.2.43', 5, 'routine', .98),
    ]
    events = []
    for text, count, importance, confidence in samples:
        for _ in range(count):
            stamp = (datetime.datetime(2026, 9, 21, 12, 41, tzinfo=datetime.timezone.utc) + datetime.timedelta(seconds=len(events))).isoformat().replace('+00:00', 'Z')
            event = parse_stream(io.BytesIO((stamp+' '+text+'\n').encode()),
                                 {'type':'kubernetes','context':'demo-cluster','namespace':'demo','pod':'api-7bd4','container':'app'}, 10)[0][0]
            event.update(id='synthetic-'+str(len(events)), importance=importance, importance_confidence=confidence,
                         category='dependency' if importance != 'routine' else 'routine', severity='degraded' if importance != 'routine' else 'info')
            events.append(event)
    report = build_report(events, [], {'context':'demo-cluster'}, 'jev', 0, 0)
    report['live'] = {'status':'running','message':'Synthetic live window | 3 containers | Jev semantic search available','queue_depth':0,'dropped':0}
    return report


def synthetic_search(client, events, query):
    client.usage.begin()
    client.usage.record(json.dumps({'usage':{'input_tokens':900,'output_tokens':40}}))
    results = []
    for event in events:
        if '192.0.2.42' in query:
            match, confidence = '192.0.2.42' in event['text'], .98
        else:
            match, confidence = 'database' in event['text'], event['importance_confidence']
        results.append({'relevance':'match' if match else 'unrelated','confidence':confidence})
    return results


def main():
    ffmpeg = shutil.which('ffmpeg')
    font = Path(os.environ.get('DEMO_FONT', '/System/Library/Fonts/Menlo.ttc'))
    if not ffmpeg or not font.is_file(): raise SystemExit('Requires ffmpeg and a monospace DEMO_FONT')
    temp = Path(tempfile.mkdtemp(prefix='jevernetes-terminal-search-'))
    shutil.copyfile(font, temp/'font.ttf')
    browser, screen, report = Browser(), Screen(), fixture()
    frames, visible = [], []

    def frame(caption, duration=1, pointer=None):
        browser.draw(screen, report, curses)
        index = len(frames)
        filters = ['drawbox=x=30:y=78:w=1220:h=640:color=0x111318:t=fill',
                   'drawbox=x=30:y=78:w=1220:h=34:color=0x24212e:t=fill']
        labels = [('jevernetes / terminal', 32, 25, 25, 'c6adff'),
                  ('github.com/sunil-sadasivan/jevernetes', 803, 36, 14, 'a0a5b6'),
                  ('jevernetes k8s -f --tui', 53, 87, 14, 'b8a0ff'),
                  (caption, 32, 742, 22, 'f1f2f5'),
                  ('SYNTHETIC LOGS + JEV RESULTS | actual terminal renderer', 32, 778, 12, 'a0a5b6')]
        for row, col, text, style in screen.lines:
            x, y = 52+col*10.24, 124+row*23
            color = 'd0d4e0'
            if style & curses.A_REVERSE:
                filters.append(f'drawbox=x={x}:y={y-2}:w={len(text)*10.24}:h=23:color=0xb8a0ff:t=fill')
                color = '16121e'
            elif style & curses.A_BOLD: color = 'eee8fa'
            labels.append((text, x, y, 17, color))
        for i, (text, x, y, size, color) in enumerate(labels):
            name = f'text-{index:03}-{i:03}.txt';(temp/name).write_text(text)
            filters.append(f'drawtext=fontfile=font.ttf:textfile={name}:expansion=none:fontsize={size}:fontcolor=0x{color}:x={x}:y={y}')
        if pointer:
            x,y=pointer
            filters.append(f'drawbox=x={52+x*10.24-5}:y={124+y*23-4}:w=26:h=27:color=0xe6c781:t=2')
        script = f'frame-{index:03}.filters';(temp/script).write_text(','.join(filters))
        name = f'frame-{index:03}.png'
        subprocess.run([ffmpeg,'-hide_banner','-loglevel','error','-y','-f','lavfi','-i','color=c=0x090b10:s=1280x810',
                        '-filter_script:v',script,'-frames:v','1',name],cwd=temp,check=True)
        frames.append((name,duration));visible.append({'caption':caption,'lines':[line[2] for line in screen.lines]})

    def key(value): browser.key(value, curses)
    def tab(index, caption):
        browser.draw(screen, report, curses)
        _,y,x,_,_=browser.tabs[index]
        browser.key(curses.KEY_MOUSE,curses,(0,x+2,y,0,curses.BUTTON1_PRESSED))
        frame(caption,1.2,(x+2,y))
    def type_query(query, caption):
        key('/');key(21)
        for i in range(0,len(query),4):
            for char in query[i:i+4]:key(char)
            frame(caption,.1)
    def submit():
        key(10);browser.search_worker.join(5)
        assert browser.search_run.snapshot()['status']=='complete'
        browser.draw(screen, report, curses)

    with patch('jevernetes.search.api_key',return_value='synthetic-demo'), \
         patch('jevernetes.jev.Jev.search',synthetic_search), \
         patch('urllib.request.OpenerDirector.open',side_effect=AssertionError('Network forbidden in terminal demo')):
        frame('Your Kubernetes logs. Right in the terminal.',1.8)
        tab(1,'Click tabs to switch between log filters.')
        tab(2,'Keep uncertain events in view.')
        tab(0,'Focus on the events worth investigating.')
        type_query('major issue with db','Press / and ask Jev a question.')
        submit()
        assert browser.search_run.snapshot()['matched_groups']==3
        assert browser.rows[0]['search_match']['count']==24
        frame('Matching groups, relevance confidence and timestamps.',3)
        key('i');browser.draw(screen,report,curses);assert len(browser.rows)==24
        frame('Press i to see all 24 instances.',2.3)
        key(10);assert 'db/pool.py:42' in browser.detail['text']
        frame('Enter opens the full log, including stack traces.',2.3)
        key(27);key(27)
        type_query('requests from 192.0.2.42','Find requests from an IP address.')
        submit();assert browser.search_run.snapshot()['matching_instances']==10
        frame('Search includes routine logs, too.',2.6)
        key('/');submit();assert browser.search_run.snapshot()['usage']['request_attempts']==0
        frame('Repeat a question. Reuse cached relevance checks.',2.8)
        frame('Ask. Inspect. Stay in the terminal.',2.5)
    entries=''.join(f"file '{name}'\nduration {duration}\n" for name,duration in frames)
    (temp/'frames.txt').write_text(entries+f"file '{frames[-1][0]}'\n")
    (temp/'visible-text.json').write_text(json.dumps(visible,indent=2))
    output=ROOT/'docs/images';output.mkdir(parents=True,exist_ok=True)
    duration=sum(duration for _,duration in frames)
    for extension in ('gif','mp4'):
        options=['-filter_complex','[0:v]fps=10,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=3','-loop','0'] if extension=='gif' else ['-vf','fps=30,format=yuv420p','-c:v','libx264','-crf','20','-movflags','+faststart']
        subprocess.run([ffmpeg,'-hide_banner','-loglevel','error','-y','-f','concat','-safe','0','-i','frames.txt',*options,'-t',str(duration),str(output/f'terminal-search-demo.{extension}')],cwd=temp,check=True)
    preview=next(i for i,item in enumerate(visible) if item['caption'].startswith('Matching groups'))
    shutil.copyfile(temp/frames[preview][0],output/'terminal-search-demo-preview.png')
    print(json.dumps({'review_frames':str(temp),'seconds':duration,'output':str(output)}))


if __name__=='__main__': main()
