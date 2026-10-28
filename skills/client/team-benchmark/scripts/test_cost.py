"""Unit tests for cost.py on synthetic turns: python3 -m unittest discover -s <this dir>."""
import math, os, sys, unittest

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import benchmark  # noqa: E402
import cost  # noqa: E402
import datetime as d  # noqa: E402

MISS, HIT, OUT = 0.15e-6, 0.003e-6, 0.60e-6


def turn(seat, i, miss, hit, out, session='s1', activity='other'):
    return {'seat': seat, 'session': session, 'ts': f'2026-10-28T00:{i // 60:02d}:{i % 60:02d}Z', 'event': 'turn',
            'tokens_in': miss, 'cache_read': hit, 'tokens_out': out, 'activity': activity,
            'cost_usd': miss * MISS + hit * HIT + out * OUT}


def sawtooth(seat='a', cycles=3, S=30_000, C=16_000, g=2_000, peak=90_000):
    """A seat that starts at S, grows g a turn to peak, compacts back to S with C miss tokens."""
    turns, i, c = [], 0, S
    for _ in range(cycles):
        turns.append(turn(seat, i, C, S - C, 500))
        i, c = i + 1, S
        while c + g <= peak:
            c += g
            turns.append(turn(seat, i, 1_000, c - 1_000, 400 + i % 7 * 50))
            i += 1
    return turns


class PriceFit(unittest.TestCase):
    def test_recovers_exact_prices(self):
        p = cost.fit_prices(sawtooth())
        self.assertAlmostEqual(p['miss'], 0.15, places=6)
        self.assertAlmostEqual(p['hit'], 0.003, places=6)
        self.assertAlmostEqual(p['out'], 0.60, places=6)

    def test_singular_is_none(self):
        self.assertIsNone(cost.fit_prices([turn('a', 0, 0, 0, 0)]))
        self.assertIsNone(cost.fit_prices([]))


class Compactions(unittest.TestCase):
    def test_sawtooth_counts_each_drop(self):
        comp = cost.compactions(sawtooth(cycles=3))
        self.assertEqual(len(comp), 2)                      # the first cycle starts the session
        self.assertEqual(comp[0], (90_000, 30_000, 16_000))  # peak, after, rebuild miss

    def test_small_context_and_shallow_drop_are_not_compactions(self):
        small = [turn('a', 0, 0, 39_000, 1), turn('a', 1, 0, 1_000, 1)]  # previous <= 40k
        shallow = [turn('a', 0, 0, 100_000, 1), turn('a', 1, 0, 61_000, 1)]  # 0.61x
        self.assertEqual(cost.compactions(small), [])
        self.assertEqual(cost.compactions(shallow), [])

    def test_growth_ignores_drops_and_dumps(self):
        seq = [turn('a', i, 0, c, 1) for i, c in enumerate((10_000, 12_000, 5_000, 70_000, 71_000))]
        self.assertEqual(cost.growths(seq), [2_000, 1_000])

    def test_seats_are_separate(self):
        turns = sawtooth('a', 2) + sawtooth('b', 2)
        a = cost.analyse(turns, 24)
        self.assertEqual(a['compaction']['count'], 2)
        self.assertEqual(a['seats']['a']['compactions'], 1)
        self.assertEqual(a['compaction']['S'], 30_000)
        self.assertEqual(a['compaction']['C'], 16_000)


class Optimum(unittest.TestCase):
    def test_closed_form_is_the_minimum(self):
        S, C, g, w = 32_000, 16_000, 1_500, 0.02
        T = cost.optimum_trigger(S, C, g, w)
        self.assertAlmostEqual(T - S, math.sqrt(2 * C * g / w))
        f = cost.avg_input_cost(T, S, C, g, w)
        for dT in (-5_000, -500, 500, 5_000):
            self.assertGreater(cost.avg_input_cost(T + dT, S, C, g, w), f)

    def test_analyse_reports_relative_costs(self):
        a = cost.analyse(sawtooth(cycles=4), 24)
        m = a['compaction']
        self.assertAlmostEqual(a['w_r'], 0.02, places=4)
        self.assertEqual(m['g_mean'], 2_000)
        self.assertEqual(m['T_opt'], round(cost.optimum_trigger(30_000, 16_000, 2_000, 0.02)))
        self.assertGreaterEqual(m['f_peak'], 1.0)
        self.assertGreater(m['f_500k'], m['f_250k'])
        self.assertAlmostEqual(m['breakeven_turns'], round(16_000 / (0.02 * 60_000), 1))


class Split(unittest.TestCase):
    def test_split_and_hit_ratio(self):
        a = cost.analyse(sawtooth(), 12)
        self.assertAlmostEqual(sum(a['split_pct'].values()), 100, delta=0.2)
        self.assertEqual(a['usd_day'], round(a['usd'] * 2, 4))
        self.assertTrue(0.9 < a['hit_ratio'] < 1)


class BeforeAfter(unittest.TestCase):
    def run_row(self, at, usd):
        return {'kind': 'run', 'at': at, 'cost': {'usd_day': usd, 'turns': 10, 'coord': 0.2, 'seats': {'lead': {'usd': usd}},
                                                   'compaction': {'count': 3}},
                'productivity': {'usd_merge': usd / 10, 'merges': 10, 'scratch': 1}}

    def test_compares_with_the_run_before_the_latest_change(self):
        hist = [self.run_row('2026-10-26T00:00:00+00:00', 8.0),
                {'kind': 'change', 'ts': '2026-10-26T01:00:00+00:00', 'text': 'one'},
                {'kind': 'change', 'ts': '2026-10-27T01:00:00+00:00', 'text': 'two'}]
        now = d.datetime(2026, 10, 27, 12, tzinfo=d.timezone.utc)
        out = '\n'.join(benchmark.compare(hist, self.run_row(now.isoformat(), 6.0), now))
        self.assertIn('change "two" 11h ago', out)
        self.assertIn('WARN only 11h', out)
        self.assertIn('WARN 2 changes', out)
        self.assertIn('-25%', out)


if __name__ == '__main__':
    unittest.main()
