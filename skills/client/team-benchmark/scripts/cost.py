"""Cost and performance model of a room, from `4genteam room metrics ROOM --since Hh --print`.

Pure python3 stdlib, no I/O except `load_metrics` (runs the binary) and `git_waste` (runs git), so the
math is testable on a synthetic turn list. Token units: tokens_in = cache-miss input, cache_read =
cache-hit input, tokens_out = output. "miss tokens" is the unit of the compaction model: one hit token
costs w_r = hit price / miss price miss tokens.
"""
import collections, json, math, re, statistics, subprocess, time

COMPACT_DROP = 0.6      # a compaction: context falls below 0.6x the previous turn ...
COMPACT_MIN = 40_000    # ... and the previous turn carried more than 40k
GROWTH_MAX = 50_000     # per-turn growth above this is a file dump or a new session, not growth
COORD = ('queue-poll', 'queue-write')
SCRATCH = re.compile(r'^(gate-|proof-|tmp-|.*-rebased)')


def load_metrics(room, hours, path=None):
    """Turn events of the room, from a saved --print file or from the binary."""
    if path:
        text = open(path).read()
    else:
        text = subprocess.run(['4genteam', 'room', 'metrics', room, '--since', f'{hours}h', '--print'],
                              capture_output=True, text=True, check=True).stdout
    turns = []
    for line in text.splitlines():
        try:
            r = json.loads(line)
        except ValueError:
            continue
        if r.get('event') == 'turn':
            turns.append(r)
    return turns


def ctx(t):
    return (t.get('tokens_in') or 0) + (t.get('cache_read') or 0)


def solve3(a, b):
    """Solve the 3x3 system a x = b by Gaussian elimination with partial pivoting; None if singular."""
    m = [row[:] + [b[i]] for i, row in enumerate(a)]
    for c in range(3):
        p = max(range(c, 3), key=lambda r: abs(m[r][c]))
        if abs(m[p][c]) < 1e-300:
            return None
        m[c], m[p] = m[p], m[c]
        for r in range(3):
            if r != c:
                f = m[r][c] / m[c][c]
                m[r] = [x - f * y for x, y in zip(m[r], m[c])]
    return [m[i][3] / m[i][i] for i in range(3)]


def fit_prices(turns):
    """Least squares of cost_usd on (tokens_in, cache_read, tokens_out), no intercept.
    Returns USD per 1M tokens {'miss', 'hit', 'out'} or None when the system is singular."""
    keys = ('tokens_in', 'cache_read', 'tokens_out')
    rows = [([float(t.get(k) or 0) for k in keys], float(t.get('cost_usd') or 0)) for t in turns]
    ata = [[sum(x[i] * x[j] for x, _ in rows) for j in range(3)] for i in range(3)]
    aty = [sum(x[i] * y for x, y in rows) for i in range(3)]
    sol = solve3(ata, aty) if rows else None
    if not sol:
        return None
    return dict(zip(('miss', 'hit', 'out'), (v * 1e6 for v in sol)))


def by_seat(turns):
    seats = collections.defaultdict(list)
    for t in turns:
        seats[t.get('seat', '?')].append(t)
    for s in seats.values():
        s.sort(key=lambda t: t.get('ts', ''))
    return dict(sorted(seats.items()))


def compactions(seq):
    """Compactions in one seat's turns, sorted by ts: (peak, after, miss on the turn after)."""
    out = []
    for prev, cur in zip(seq, seq[1:]):
        p, c = ctx(prev), ctx(cur)
        if p > COMPACT_MIN and c < COMPACT_DROP * p:
            out.append((p, c, cur.get('tokens_in') or 0))
    return out


def growths(seq):
    """Positive per-turn context growth in (0, GROWTH_MAX]."""
    return [d for d in (ctx(b) - ctx(a) for a, b in zip(seq, seq[1:])) if 0 < d <= GROWTH_MAX]


def session_starts(turns):
    first = {}
    for t in sorted(turns, key=lambda t: t.get('ts', '')):
        first.setdefault((t.get('seat'), t.get('session')), ctx(t))
    return list(first.values())


def avg_input_cost(T, S, C, g, w_r):
    """Average input cost per turn in miss tokens when a seat compacts at context T back to S:
    the context grows by g a turn, so a cycle lasts (T-S)/g turns at mean context (S+T)/2 read
    from cache (w_r each), plus one rebuild of C miss tokens per cycle."""
    return w_r * (S + T) / 2 + C * g / (T - S)


def optimum_trigger(S, C, g, w_r):
    """T minimising avg_input_cost: d/dT = w_r/2 - C g/(T-S)^2 = 0  =>  T - S = sqrt(2 C g / w_r)."""
    return S + math.sqrt(2 * C * g / w_r)


def med(xs):
    return statistics.median(xs) if xs else 0


def pctl(xs, q):
    if not xs:
        return 0
    s = sorted(xs)
    return s[min(len(s) - 1, int(q * len(s)))]


def analyse(turns, hours):
    """Every number of the cost section, as one JSON-ready dict."""
    usd = sum(t.get('cost_usd') or 0 for t in turns)
    miss = sum(t.get('tokens_in') or 0 for t in turns)
    hit = sum(t.get('cache_read') or 0 for t in turns)
    out = sum(t.get('tokens_out') or 0 for t in turns)
    prices = fit_prices(turns) or {'miss': 0, 'hit': 0, 'out': 0}
    split_usd = {k: prices[k] * n / 1e6 for k, n in (('miss', miss), ('hit', hit), ('out', out))}
    fit_total = sum(split_usd.values()) or 1
    w_r = prices['hit'] / prices['miss'] if prices['miss'] > 0 else 0
    ctxs = [ctx(t) for t in turns]

    seats, all_comp, all_g = {}, [], []
    for seat, seq in by_seat(turns).items():
        comp, g = compactions(seq), growths(seq)
        all_comp += comp
        all_g += g
        s_usd = sum(t.get('cost_usd') or 0 for t in seq)
        act = collections.defaultdict(lambda: [0, 0.0])
        for t in seq:
            a = act[t.get('activity') or 'other']
            a[0] += 1
            a[1] += t.get('cost_usd') or 0
        n = len(seq)
        seats[seat] = {
            'turns': n, 'usd': round(s_usd, 4), 'share': round(s_usd / usd, 4) if usd else 0,
            'usd_turn': round(s_usd / n, 6) if n else 0,
            'out_turn': round(sum(t.get('tokens_out') or 0 for t in seq) / n) if n else 0,
            'miss_turn': round(sum(t.get('tokens_in') or 0 for t in seq) / n) if n else 0,
            'compactions': len(comp), 'peak': round(med([c[0] for c in comp])),
            'after': round(med([c[1] for c in comp])),
            'coord': round(sum(act[k][0] for k in COORD if k in act) / n, 4) if n else 0,
            'activity': {k: [v[0], round(v[1], 4)] for k, v in sorted(act.items(), key=lambda kv: -kv[1][1])},
        }

    S = med([c[1] for c in all_comp])
    C = med([c[2] for c in all_comp])
    g_med, g_mean = med(all_g), (statistics.mean(all_g) if all_g else 0)
    peak = med([c[0] for c in all_comp])
    model = {'count': len(all_comp), 'peak': round(peak), 'S': round(S), 'C': round(C),
             'g_median': round(g_med), 'g_mean': round(g_mean),
             'init': round(med(session_starts(turns)))}
    if all_comp and g_mean > 0 and w_r > 0 and C > 0:
        T = optimum_trigger(S, C, g_mean, w_r)
        f0 = avg_input_cost(T, S, C, g_mean, w_r)
        rel = lambda x: round(avg_input_cost(x, S, C, g_mean, w_r) / f0, 2) if x > S else None
        model.update({'T_opt': round(T), 'f_peak': rel(peak), 'f_250k': rel(250_000), 'f_500k': rel(500_000),
                      'breakeven_turns': round(C / (w_r * (peak - S)), 1) if peak > S else None,
                      'cycle_turns': round((peak - S) / g_mean, 1)})

    coord = sum(1 for t in turns if t.get('activity') in COORD)
    return {
        'turns': len(turns), 'usd': round(usd, 4), 'usd_day': round(usd * 24 / hours, 4) if hours else 0,
        'prices': {k: round(v, 4) for k, v in prices.items()}, 'w_r': round(w_r, 4),
        'split_usd': {k: round(v, 4) for k, v in split_usd.items()},
        'split_pct': {k: round(100 * v / fit_total, 1) for k, v in split_usd.items()},
        'hit_ratio': round(hit / (hit + miss), 4) if hit + miss else 0,
        'ctx': {'mean': round(statistics.mean(ctxs)) if ctxs else 0, 'median': round(med(ctxs)),
                'p90': pctl(ctxs, 0.9), 'max': max(ctxs, default=0)},
        'coord': round(coord / len(turns), 4) if turns else 0,
        'compaction': model, 'seats': seats,
    }


def git_out(repo, *args):
    return subprocess.run(['git', '-C', repo, *args], capture_output=True, text=True).stdout


def main_branch(repo):
    for b in ('main', 'master'):
        if git_out(repo, 'rev-parse', '--verify', '-q', f'refs/heads/{b}').strip():
            return b
    return None


def git_waste(repo, hours):
    """Merges into main and branch waste in one project repo. Only branches whose tip was committed
    in the window count, so the numbers describe this window and not the repo's whole history."""
    main = main_branch(repo)
    if not main:
        return None
    since = f'--since={hours} hours ago'
    merges = int(git_out(repo, 'rev-list', '--count', '--merges', '--first-parent', since, main).strip() or 0)
    rows = git_out(repo, 'for-each-ref', '--format=%(refname:short) %(committerdate:unix)', 'refs/heads').split()
    cut = time.time() - hours * 3600
    branches = [rows[i] for i in range(0, len(rows), 2) if rows[i] != main and int(rows[i + 1]) >= cut]
    named, in_main, patches = [], [], {}
    for b in branches:
        if SCRATCH.match(b):
            named.append(b)
        left = git_out(repo, 'rev-list', '--count', '--no-merges', '--cherry-pick', '--right-only',
                       f'{main}...{b}').strip()
        if left == '0':
            in_main.append(b)
            continue
        diff = git_out(repo, 'log', '--no-merges', '-p', f'{main}..{b}')
        ids = subprocess.run(['git', '-C', repo, 'patch-id', '--stable'], input=diff,
                             capture_output=True, text=True).stdout.split()[0::2]
        if ids:
            patches[b] = set(ids)
    dup = sorted(b for b, s in patches.items() if any(o != b and s <= t for o, t in patches.items()))
    return {'path': repo, 'main': main, 'merges': merges, 'branches': len(branches),
            'scratch_named': sorted(named), 'in_main': sorted(in_main), 'duplicates': dup,
            'scratch': len(set(named) | set(in_main) | set(dup))}
