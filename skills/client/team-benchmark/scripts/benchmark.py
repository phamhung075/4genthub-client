#!/usr/bin/env python3
"""Measure a room's last N hours: where each seat's tool calls go, and what its commits produced.

Reads <state-root>/rooms/<room>/seats/<seat>/tool-calls.jsonl and `git log` of the hub, the client
and every project repo the room works in. A project repo is found through the git worktrees under
<state-root>/rooms/<room>/wt/ (or named with --repo); its log covers the main checkout's HEAD and the
HEAD of each of the room's worktrees, so seat branches that are not merged yet still count.
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


def git(repo, *args):
    return subprocess.run(['git', '-C', repo, *args], capture_output=True, text=True, check=True).stdout


def common_dir(path):
    try:
        return os.path.realpath(os.path.join(path, git(path, 'rev-parse', '--git-common-dir').strip()))
    except (subprocess.CalledProcessError, OSError):
        return None


def room_heads(repo, wt_root):
    """The repo's own HEAD plus the HEAD of each of its worktrees that lies under the room's wt/."""
    heads, path = [git(repo, 'rev-parse', 'HEAD').strip()], None
    root = os.path.realpath(wt_root) + os.sep
    for line in git(repo, 'worktree', 'list', '--porcelain').splitlines():
        if line.startswith('worktree '):
            path = os.path.realpath(line[len('worktree '):])
        elif line.startswith('HEAD ') and path and path.startswith(root):
            heads.append(line[len('HEAD '):])
    return sorted(set(heads), key=heads.index)


def project_repos(wt_root, known):
    """Repos other than the hub and the client that have a worktree under the room's wt/ (depth 2)."""
    found = {}
    if not os.path.isdir(wt_root):
        return []
    todo = [(os.path.join(wt_root, n), 1) for n in sorted(os.listdir(wt_root))]
    while todo:
        path, depth = todo.pop(0)
        if not os.path.isdir(path) or os.path.islink(path):
            continue
        if os.path.exists(os.path.join(path, '.git')):
            cd = common_dir(path)
            if cd and cd not in known and cd not in found:
                main = os.path.dirname(cd) if os.path.basename(cd) == '.git' else path
                found[cd] = main
            continue
        if depth < 2:
            todo += [(os.path.join(path, n), depth + 1) for n in sorted(os.listdir(path))]
    return list(found.values())


def commit_lines(repo, hours, wt_root):
    heads = room_heads(repo, wt_root)
    since = f'--since={hours} hours ago'
    log = git(repo, 'log', since, '--no-merges', '--numstat', '--format=@@%h', *heads)
    merges = git(repo, 'rev-list', '--count', since, '--merges', *heads).strip()
    commits, lines = log.count('@@'), collections.Counter()
    for line in log.splitlines():
        m = re.match(r'(\d+)\t\d+\t(.*)', line)
        if m:
            lines[file_kind(m[2])] += int(m[1])
    return {'path': os.path.realpath(repo), 'heads': len(heads), 'commits': commits,
            'merges': int(merges), **lines}


def pct(n, total):
    return f'{100 * n // total}%' if total else '-'


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('room')
    ap.add_argument('--hours', type=int, default=24)
    ap.add_argument('--state-root', default=os.path.expanduser('~/.config/4genthub'))
    ap.add_argument('--hub', default='.')
    ap.add_argument('--client', default='agenthub_client')
    ap.add_argument('--repo', action='append', default=[],
                    help='project repo to count (repeatable); default: found from the room worktrees')
    a = ap.parse_args()

    wt_root = f'{a.state_root}/rooms/{a.room}/wt'
    seats = seat_calls(a.state_root, a.room, a.hours)
    repos = {'hub': commit_lines(a.hub, a.hours, wt_root), 'client': commit_lines(a.client, a.hours, wt_root)}
    known = {common_dir(a.hub), common_dir(a.client)}
    for path in a.repo or project_repos(wt_root, known):
        name = os.path.basename(os.path.realpath(path))
        while name in repos:
            name += '+'
        repos[name] = commit_lines(path, a.hours, wt_root)
    result = {'at': d.datetime.now(d.timezone.utc).isoformat(timespec='seconds'), 'hours': a.hours,
              'seats': seats, **repos, 'repos': list(repos)}

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
    if prev:
        print(f"(prev run {prev['at']})")
    else:
        print(f'(no previous run in {hist})')
    meta = ('path', 'heads', 'commits', 'merges')
    for repo, r in repos.items():
        tot = sum(v for k, v in r.items() if k not in meta)
        kinds = ' '.join(f'{k}={pct(v, tot)}' for k, v in r.items() if k not in meta)
        old = prev.get(repo) if prev else None
        delta = f"  (prev commits={old['commits']} added={sum(v for k, v in old.items() if k not in meta)})" if old else ''
        print(f"{repo:6} commits={r['commits']} merges={r['merges']} heads={r['heads']} added={tot} {kinds}{delta}")


if __name__ == '__main__':
    main()
