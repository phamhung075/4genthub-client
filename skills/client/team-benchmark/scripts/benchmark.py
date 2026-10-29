#!/usr/bin/env python3
"""Measure a room's last N hours: where each seat's tool calls go, and what its commits produced.

Reads <state-root>/rooms/<room>/seats/<seat>/tool-calls.jsonl and `git log` of the hub, the client
and every project repo the room works in. A project repo is found through the git worktrees under
<state-root>/rooms/<room>/wt/ (or named with --repo); its log covers the main checkout's HEAD and the
HEAD of each of the room's worktrees, so seat branches that are not merged yet still count.
It also reads the room's turn metrics (`4genteam room metrics ROOM --since Hh --print`) for the cost
model in cost.py: fitted prices, spend split, per-seat cost and activity, the compaction optimum, and
merges and branch waste in each project repo.
Prints a brief report (default, ~25 lines), the full one (--full) or one JSON object (--json), and
appends one JSON line to <state-root>/benchmarks/<room>.jsonl; `--change TEXT` appends a change
marker instead, and every run compares itself with the last run before the latest marker.
Reads no .env and no token.
"""
import argparse, collections, datetime as d, json, os, re, subprocess, sys

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import cost  # noqa: E402

CHAT = re.compile(r'4genteam (room queue|send|room send|input)|queue (create|update|close|claim)')
TEST = re.compile(r'\bgo test|cargo test|cargo-slot\.sh\s+test|npx (tsc|vitest|playwright)|pytest|go vet|go build')


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
    """Repos other than the hub and the client that have a worktree under the room's wt/ (depth 2).
    A seat's wt/<seat> is often a hub worktree with the project checked out inside it
    (wt/lead/aa2-rs), so the walk goes on into a hub or client worktree instead of stopping there."""
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
            if cd not in known:
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


def k(n):
    return f'{n / 1000:.0f}k' if n >= 10_000 else f'{n / 1000:.1f}k'


def default_repos():
    """The client is the main checkout of the repo this script lives in (a client worktree resolves to
    it through its common dir); the hub is the superproject that holds that checkout as a submodule."""
    cd = common_dir(os.path.dirname(os.path.realpath(__file__)))
    if not cd:
        return None, None
    if os.path.basename(cd) == '.git':
        client = os.path.dirname(cd)
    else:
        client = git(os.path.dirname(os.path.realpath(__file__)), 'rev-parse', '--show-toplevel').strip()
    hub = git(client, 'rev-parse', '--show-superproject-working-tree').strip()
    if not hub and '/.git/modules/' in cd:
        hub = cd.split('/.git/modules/')[0]
    return hub or None, client


def read_history(hist):
    rows = []
    if os.path.exists(hist):
        for line in open(hist):
            try:
                rows.append(json.loads(line))
            except ValueError:
                pass
    return rows


def flat(run):
    """The numbers the before/after table compares, from one history row (old rows give None)."""
    c, p = run.get('cost') or {}, run.get('productivity') or {}
    seats = {s: v.get('usd') for s, v in (c.get('seats') or {}).items()}
    return {'usd/day': c.get('usd_day'), 'usd/merge': p.get('usd_merge'), 'merges': p.get('merges'),
            'turns': c.get('turns'), 'coord share': c.get('coord'),
            'compactions': (c.get('compaction') or {}).get('count'), 'scratch branches': p.get('scratch'),
            'seats': seats}


def delta(a, b):
    if a is None or b is None:
        return '-'
    if a == 0:
        return 'new' if b else '0%'
    return f'{100 * (b - a) / a:+.0f}%'


def fmt(v):
    if v is None:
        return '-'
    if isinstance(v, float):
        return f'{v:.3f}' if v < 1 else f'{v:.2f}'
    return str(v)


def compare(history, result, now):
    """Lines comparing this run with the last run before the latest change marker."""
    marks = [r for r in history if r.get('kind') == 'change']
    runs = [r for r in history if r.get('kind', 'run') == 'run']
    if marks:
        m = marks[-1]
        base = [r for r in runs if r['at'] < m['ts']]
        if not base:
            return [f'before/after: no run before the change "{m["text"]}" ({m["ts"]}); nothing to compare']
        base = base[-1]
        hrs = (now - d.datetime.fromisoformat(m['ts'])).total_seconds() / 3600
        head = [f'change "{m["text"]}" {hrs:.0f}h ago; before = run {base["at"]}']
        if hrs < 24:
            head.append(f'WARN only {hrs:.0f}h since the change: wait for 24h before judging it')
        between = [x for x in marks if x['ts'] > base['at']]
        if len(between) >= 2:
            head.append(f'WARN {len(between)} changes since the before run: the cause is not separable')
    elif runs:
        base = runs[-1]
        head = [f'no change marker; vs previous run {base["at"]} (mark a change with --change "<text>")']
    else:
        return ['before/after: first run, nothing to compare']
    a, b = flat(base), flat(result)
    lines = head + [f'  {"metric":17}{"before":>9}{"after":>9}{"delta":>7}']
    for key in ('usd/day', 'usd/merge', 'merges', 'turns', 'coord share', 'compactions', 'scratch branches'):
        lines.append(f'  {key:17}{fmt(a[key]):>9}{fmt(b[key]):>9}{delta(a[key], b[key]):>7}')
    seats = sorted(set(a['seats']) | set(b['seats']))
    if seats:
        lines.append('  seat usd ' + ', '.join(f'{s} {fmt(a["seats"].get(s))} -> {fmt(b["seats"].get(s))} '
                                              f'({delta(a["seats"].get(s), b["seats"].get(s))})' for s in seats))
    return lines


def _ts(value):
    return d.datetime.fromisoformat(value.replace('Z', '+00:00'))


def wait_stats(path, hours, now):
    """Declared seat waits (`4genteam wait`) in the window, from <state-root>/rooms/<room>/waits.jsonl.

    A start and its end make one interval; a start with no end yet runs to now (at most its max). Each interval is
    clipped to the window. Answers per-seat waiting seconds and share, totals by kind and by kind:target, and how
    the waits ended - so the director sees where the pipeline blocks."""
    lo = now - d.timedelta(hours=hours)
    open_, spans, ended = {}, [], collections.Counter()
    try:
        lines = open(path).read().splitlines()
    except OSError:
        return None
    for line in lines:
        try:
            e = json.loads(line)
        except ValueError:
            continue
        seat, ev = e.get('seat'), e.get('event')
        if ev == 'start':
            open_[seat] = e
        elif ev == 'end':
            start = open_.pop(seat, None)
            t1 = _ts(e['ts'])
            t0 = t1 - d.timedelta(seconds=e.get('duration_s', 0))
            spans.append((seat, e.get('kind'), e.get('target'), t0, t1))
            if t1 >= lo:
                ended[e.get('how', '?')] += 1
    for seat, e in open_.items():
        t0 = _ts(e['ts'])
        spans.append((seat, e.get('kind'), e.get('target'), t0, min(now, t0 + d.timedelta(seconds=e.get('max_s', 0)))))
    seats, kinds, targets, counts = collections.Counter(), collections.Counter(), collections.Counter(), collections.Counter()
    for seat, kind, target, t0, t1 in spans:
        sec = (min(t1, now) - max(t0, lo)).total_seconds()
        if sec <= 0:
            continue
        seats[seat] += sec
        counts[seat] += 1
        kinds[kind] += sec
        targets[f'{kind}:{target}'] += sec
    window = hours * 3600
    return {'seats': {s: {'seconds': round(v), 'share': round(v / window, 4), 'waits': counts[s]} for s, v in seats.most_common()},
            'kinds': {k: round(v) for k, v in kinds.most_common()},
            'targets': {k: round(v) for k, v in targets.most_common(5)},
            'ended': dict(ended)}


def hm(seconds):
    m = int(seconds // 60)
    return f'{m // 60}h{m % 60:02d}m' if m >= 60 else f'{m}m'


def wait_line(w):
    if not w or not w['seats']:
        return ['waits: none declared in the window']
    seats = ', '.join(f"{s} {100 * v['share']:.0f}% ({v['waits']}x)" for s, v in w['seats'].items())
    kinds = ', '.join(f'{k} {hm(v)}' for k, v in w['kinds'].items())
    tops = ', '.join(f'{k} {hm(v)}' for k, v in list(w['targets'].items())[:3])
    ended = ', '.join(f'{k} {v}' for k, v in sorted(w['ended'].items()))
    return [f'waits: {seats}; by kind {kinds}; top {tops}' + (f'; ended {ended}' if ended else '')]


def brief(room, hours, c, prod, waste):
    out = []
    if c:
        sp, pr = c['split_pct'], c['prices']
        out.append(f"{room} {hours}h: ${c['usd']:.2f} (${c['usd_day']:.2f}/day) {c['turns']} turns, "
                   f"{prod['merges']} merges, ${fmt(prod['usd_merge'])}/merge, {prod['commits']} commits; "
                   f"hit ratio {c['hit_ratio']:.3f}; spend out {sp['out']:.0f}% miss {sp['miss']:.0f}% "
                   f"hit {sp['hit']:.0f}%; $/1M miss {pr['miss']:.3f} hit {pr['hit']:.4f} out {pr['out']:.3f}")
        for seat, s in c['seats'].items():
            top = ', '.join(f'{a} ${v[1]:.2f}' for a, v in list(s['activity'].items())[:2])
            out.append(f"  {seat:10} {s['turns']:5}t ${s['usd']:.2f} ({100 * s['share']:.0f}%) "
                       f"${s['usd_turn']:.5f}/t out {s['out_turn']}/t miss {s['miss_turn']}/t "
                       f"coord {100 * s['coord']:.0f}% comp {s['compactions']} | {top}")
        m = c['compaction']
        line = (f"compaction {m['count']}x: peak {k(m['peak'])} -> S {k(m['S'])} (init {k(m['init'])}), "
                f"C {k(m['C'])} miss, g {k(m['g_mean'])}/turn (median {k(m['g_median'])}), w_r {c['w_r']}")
        if 'T_opt' in m:
            line += (f"; T_opt {k(m['T_opt'])} vs peak {k(m['peak'])}: cost x{m['f_peak']} "
                     f"(250k x{m['f_250k']}, 500k x{m['f_500k']}); break-even {m['breakeven_turns']} "
                     f"of {m['cycle_turns']} turns/cycle")
        out.append(line)
    else:
        out.append(f'{room} {hours}h: no turn metrics (4genteam room metrics failed); cost section skipped')
    for w in waste:
        out.append(f"waste {os.path.basename(w['path'])}: {w['scratch']} of {w['branches']} branches active "
                   f"in the window are scratch (named {len(w['scratch_named'])}, already in {w['main']} "
                   f"{len(w['in_main'])}, duplicate {len(w['duplicates'])})")
    return out


def full(room, hours, c, prod, waste, seats, repos, prev):
    out = [f'{room}, last {hours}h (share of tool calls)']
    for seat, s in seats.items():
        n = s['calls']
        cols = ' '.join(f'{k}={pct(s.get(k, 0), n)}' for k in ('chat', 'board', 'git', 'write', 'test', 'read', 'shell'))
        old = (prev or {}).get('seats', {}).get(seat)
        dl = f"  (prev chat={pct(old.get('chat', 0), old['calls'])} write={pct(old.get('write', 0), old['calls'])})" if old else ''
        out.append(f"{seat:10} calls={n:5} notok={pct(s['notok'], n)} active_h={s['active_hours']:2} {cols}{dl}")
    meta = ('path', 'heads', 'commits', 'merges')
    for repo, r in repos.items():
        tot = sum(v for x, v in r.items() if x not in meta)
        kinds = ' '.join(f'{x}={pct(v, tot)}' for x, v in r.items() if x not in meta)
        old = (prev or {}).get(repo)
        dl = f"  (prev commits={old['commits']} added={sum(v for x, v in old.items() if x not in meta)})" if old else ''
        out.append(f"{repo:6} commits={r['commits']} merges={r['merges']} heads={r['heads']} added={tot} {kinds}{dl}")
    if c:
        cx = c['ctx']
        out.append(f"context/turn mean {k(cx['mean'])} median {k(cx['median'])} p90 {k(cx['p90'])} max {k(cx['max'])}; "
                   f"spend $ miss {c['split_usd']['miss']:.2f} hit {c['split_usd']['hit']:.2f} out {c['split_usd']['out']:.2f}; "
                   f"coord share {100 * c['coord']:.0f}%")
        for seat, s in c['seats'].items():
            acts = ', '.join(f'{a} {v[0]} ${v[1]:.2f}' for a, v in s['activity'].items())
            out.append(f"  {seat:10} peak {k(s['peak'])} after {k(s['after'])}: {acts}")
    for w in waste:
        for key in ('scratch_named', 'in_main', 'duplicates'):
            if w[key]:
                out.append(f"  {os.path.basename(w['path'])} {key}: {' '.join(w[key])}")
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('room')
    ap.add_argument('--hours', type=int, default=24)
    ap.add_argument('--state-root', default=os.path.expanduser('~/.config/4genthub'))
    hub, client = default_repos()
    ap.add_argument('--hub', default=hub, help='hub repo (default: the parent of the client checkout)')
    ap.add_argument('--client', default=client, help='client repo (default: the repo of this script)')
    ap.add_argument('--repo', action='append', default=[],
                    help='project repo to count (repeatable); default: found from the room worktrees')
    ap.add_argument('--metrics', help='read turn metrics from this saved `room metrics --print` file')
    ap.add_argument('--change', metavar='TEXT', help='append a change marker and exit (no measurement)')
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument('--brief', action='store_true', help='short report, ~25 lines (default)')
    mode.add_argument('--full', action='store_true', help='brief report plus tool-call shares, repos, activities, branch names')
    mode.add_argument('--json', action='store_true', help='one compact JSON object')
    a = ap.parse_args()

    now = d.datetime.now(d.timezone.utc)
    hist = f'{a.state_root}/benchmarks/{a.room}.jsonl'
    os.makedirs(os.path.dirname(hist), exist_ok=True)
    if a.change:
        with open(hist, 'a') as f:
            f.write(json.dumps({'kind': 'change', 'ts': now.isoformat(timespec='seconds'), 'text': a.change}) + '\n')
        print(f'change marker appended to {hist}; rerun the benchmark after >= 24h')
        return

    wt_root = f'{a.state_root}/rooms/{a.room}/wt'
    seats = seat_calls(a.state_root, a.room, a.hours)
    repos, known = {}, set()
    for name, path in (('hub', a.hub), ('client', a.client)):
        if path:
            repos[name] = commit_lines(path, a.hours, wt_root)
            known.add(common_dir(path))
    projects = a.repo or project_repos(wt_root, known)
    for path in projects:
        name = os.path.basename(os.path.realpath(path))
        while name in repos:
            name += '+'
        repos[name] = commit_lines(path, a.hours, wt_root)

    try:
        c = cost.analyse(cost.load_metrics(a.room, a.hours, a.metrics), a.hours)
    except (OSError, subprocess.CalledProcessError) as e:
        print(f'warning: no turn metrics: {e}', file=sys.stderr)
        c = None
    waste = [w for w in (cost.git_waste(os.path.realpath(p), a.hours) for p in projects) if w]
    merges = sum(w['merges'] for w in waste)
    prod = {'merges': merges, 'commits': sum(repos[n]['commits'] for n in repos if n not in ('hub', 'client')),
            'usd_merge': round(c['usd'] / merges, 4) if c and merges else None,
            'scratch': sum(w['scratch'] for w in waste)}

    waits = wait_stats(f'{a.state_root}/rooms/{a.room}/waits.jsonl', a.hours, now)
    history = read_history(hist)
    prev = next((r for r in reversed(history) if r.get('kind', 'run') == 'run'), None)
    result = {'kind': 'run', 'at': now.isoformat(timespec='seconds'), 'hours': a.hours, 'seats': seats,
              **repos, 'repos': list(repos), 'cost': c, 'productivity': prod, 'waste': waste,
              'waits': waits}
    with open(hist, 'a') as f:
        f.write(json.dumps(result) + '\n')

    if a.json:
        print(json.dumps(result, separators=(',', ':')))
        return
    lines = brief(a.room, a.hours, c, prod, waste)
    lines += wait_line(waits)
    if a.full:
        lines += full(a.room, a.hours, c, prod, waste, seats, repos, prev)
    lines += compare(history, result, now)
    print('\n'.join(lines))


if __name__ == '__main__':
    main()
