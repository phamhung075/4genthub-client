#!/usr/bin/env python3
"""Measure a room's last N hours: where each seat's tool calls go, and what its commits produced.

Reads <state-root>/rooms/<room>/seats/<seat>/tool-calls.jsonl and `git log` of the hub and the client.
Prints the tables and appends one JSON line to <state-root>/benchmarks/<room>.jsonl, so the next run
can show the change against the previous one. Reads no .env and no token.
"""
import argparse, collections, datetime as d, json, os, re, subprocess

CHAT = re.compile(r'4genteam (room queue|send|room send|input)|queue (create|update|close|claim)')
TEST = re.compile(r'\bgo test|cargo test|npx (tsc|vitest|playwright)|pytest|go vet|go build')


def category(r):
    tool, target = r['tool'], r.get('target', '')
    if tool in ('edit', 'write'):
        return 'write'
    if tool == 'read':
        return 'read'
    if tool.startswith('mcp__agenthub') or tool == 'manage_task':
        return 'board'
    if tool == 'bash':
        if CHAT.search(target):
            return 'chat'
        if TEST.search(target):
            return 'test'
        if re.search(r'\bgit\b|git_commit_capture', target):
            return 'git'
        return 'shell'
    return 'other'


def file_kind(p):
    name = p.split('/')[-1]
    if p.startswith('CHANGELOG/') or 'CHANGELOG' in name:
        return 'changelog'
    if p.startswith('ai_docs/') or p.endswith('.md'):
        return 'docs'
    if re.search(r'_test\.(go|rs)$|\.test\.tsx?$|\.spec\.ts$|/tests?/', p):
        return 'test'
    if re.search(r'\.(go|rs|tsx?|py|sh|sql|css|ya?ml|json)$', p):
        return 'code'
    return 'other'


def seat_calls(state, room, hours):
    cut = d.datetime.now(d.timezone.utc) - d.timedelta(hours=hours)
    root = f'{state}/rooms/{room}/seats'
    out = {}
    for seat in sorted(os.listdir(root)):
        path = f'{root}/{seat}/tool-calls.jsonl'
        if not os.path.exists(path):
            continue
        c, bad, hrs = collections.Counter(), 0, set()
        for line in open(path):
            try:
                r = json.loads(line)
                t = d.datetime.fromisoformat(r['at'].replace('Z', '+00:00'))
            except (ValueError, KeyError):
                continue
            if t < cut:
                continue
            c[category(r)] += 1
            bad += r.get('outcome') != 'ok'
            hrs.add(t.strftime('%d%H'))
        out[seat] = {'calls': sum(c.values()), 'notok': bad, 'active_hours': len(hrs), **c}
    return out


def commit_lines(repo, hours):
    log = subprocess.run(['git', '-C', repo, 'log', f'--since={hours} hours ago', '--no-merges',
                          '--numstat', '--format=@@%h'], capture_output=True, text=True, check=True).stdout
    commits, lines = log.count('@@'), collections.Counter()
    for line in log.splitlines():
        m = re.match(r'(\d+)\t\d+\t(.*)', line)
        if m:
            lines[file_kind(m[2])] += int(m[1])
    return {'commits': commits, **lines}


def pct(n, total):
    return f'{100 * n // total}%' if total else '-'


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('room')
    ap.add_argument('--hours', type=int, default=24)
    ap.add_argument('--state-root', default=os.path.expanduser('~/.config/4genthub'))
    ap.add_argument('--hub', default='.')
    ap.add_argument('--client', default='agenthub_client')
    a = ap.parse_args()

    seats = seat_calls(a.state_root, a.room, a.hours)
    result = {'at': d.datetime.now(d.timezone.utc).isoformat(timespec='seconds'), 'hours': a.hours,
              'seats': seats, 'hub': commit_lines(a.hub, a.hours), 'client': commit_lines(a.client, a.hours)}

    hist = f'{a.state_root}/benchmarks/{a.room}.jsonl'
    prev = None
    if os.path.exists(hist):
        rows = open(hist).read().strip().splitlines()
        prev = json.loads(rows[-1]) if rows else None
    os.makedirs(os.path.dirname(hist), exist_ok=True)
    with open(hist, 'a') as f:
        f.write(json.dumps(result) + '\n')

    print(f'{a.room}, last {a.hours}h (share of tool calls)')
    for seat, s in seats.items():
        n = s['calls']
        cols = ' '.join(f'{k}={pct(s.get(k, 0), n)}' for k in ('chat', 'board', 'git', 'write', 'test', 'read', 'shell'))
        old = prev['seats'].get(seat) if prev else None
        delta = f"  (prev chat={pct(old.get('chat', 0), old['calls'])} write={pct(old.get('write', 0), old['calls'])})" if old else ''
        print(f"{seat:10} calls={n:5} notok={pct(s['notok'], n)} active_h={s['active_hours']:2} {cols}{delta}")
    for repo in ('hub', 'client'):
        r = result[repo]
        tot = sum(v for k, v in r.items() if k != 'commits')
        kinds = ' '.join(f'{k}={pct(v, tot)}' for k, v in r.items() if k != 'commits')
        print(f"{repo:6} commits={r['commits']} added={tot} {kinds}")


if __name__ == '__main__':
    main()
