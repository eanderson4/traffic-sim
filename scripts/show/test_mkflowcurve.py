#!/usr/bin/env python3
"""Tests for mkflowcurve.py — the cumulative input-output (Newell) diagram.

The properties worth pinning are the ones whose failure is SILENT. A flow
curve that plots is a flow curve that looks right, so:

  * no bin may start after the horizon (a point outside the chart)
  * the three exit channels stay separate — a vehicle still driving at the
    cut is not a departure, and a stranded one is not a completion
  * the accumulation identity holds per bin, by construction
  * a legal-but-empty run explains itself instead of raising
"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import mkflowcurve                                            # noqa: E402


def trip(entry, exit_tick, completed, stranded=False):
    t = {"entry_tick": entry, "exit_tick": exit_tick, "completed": completed}
    if stranded:
        t["stranded"] = True          # omitempty: absent on ordinary trips
    return t


def metrics(ticks, trips, dt=0.1):
    done = sum(1 for t in trips if t["completed"])
    strand = sum(1 for t in trips if t.get("stranded"))
    active = len(trips) - done - strand
    return {"ticks": ticks, "dt": dt, "trips": trips,
            "totals": {"completed_trips": done, "stranded_trips": strand,
                       "active_at_horizon": active}}


class TestBinning(unittest.TestCase):
    def test_no_bin_starts_after_the_horizon(self):
        # The ceiling form emitted one bin past the end whenever the horizon
        # was not a whole number of bins. Consumers scale x against `ticks`,
        # so that bin plotted outside the chart.
        for ticks in (1000, 1001, 1299, 1300, 1301, 54000, 54001):
            doc = mkflowcurve.build(metrics(ticks, [trip(0, 10, True)]), 30.0)
            worst = max(b["tick"] for b in doc["bins"])
            self.assertLessEqual(
                worst, ticks,
                f"horizon {ticks}: a bin starts at {worst}, past the end")

    def test_the_last_bin_contains_the_horizon_tick(self):
        # The complement of the above: trimming must not go so far that the
        # final instant has nowhere to land.
        for ticks in (1000, 1001, 1299, 1300, 1301):
            doc = mkflowcurve.build(metrics(ticks, [trip(0, 10, True)]), 30.0)
            bt = doc["binTicks"]
            last = max(b["tick"] for b in doc["bins"])
            self.assertLessEqual(last, ticks)
            self.assertGreater(last + bt, ticks,
                               f"horizon {ticks}: tick {ticks} has no bin")

    def test_the_divisible_case_is_unchanged(self):
        # 54,000 ticks of 30 s bins at dt=0.1 is the shipped Chicago report:
        # 181 bins under both the old and the new formula, so the archived
        # figures do not move.
        doc = mkflowcurve.build(metrics(54000, [trip(0, 10, True)]), 30.0)
        self.assertEqual(doc["binTicks"], 300)
        self.assertEqual(len(doc["bins"]), 181)


class TestExitChannels(unittest.TestCase):
    def test_a_strand_on_the_final_tick_is_still_a_strand(self):
        # exit_tick == horizon. Inferring strandedness from `exit_tick <
        # horizon` classified this as still-driving, which then failed the
        # exact totals cross-check and REFUSED a valid document. The explicit
        # flag has no edge.
        trips = [trip(0, 1000, False, stranded=True)]
        doc = mkflowcurve.build(metrics(1000, trips), 30.0)
        self.assertEqual(doc["totals"]["stranded"], 1)
        self.assertEqual(doc["totals"]["activeAtHorizon"], 0)
        self.assertEqual(doc["bins"][-1]["cumStrand"], 1)
        self.assertEqual(doc["bins"][-1]["inNet"], 0)

    def test_an_incomplete_trip_without_the_flag_is_active_not_stranded(self):
        # The complement: not every incomplete trip is a strand. A vehicle
        # still driving at the cut carries no flag and must not be counted as
        # a departure even though its exit_tick is below the horizon.
        trips = [trip(0, 500, False)]
        doc = mkflowcurve.build(metrics(1000, trips), 30.0)
        self.assertEqual(doc["totals"]["stranded"], 0)
        self.assertEqual(doc["totals"]["activeAtHorizon"], 1)
        self.assertEqual(doc["bins"][-1]["inNet"], 1)

    def test_active_at_the_cut_is_not_a_departure(self):
        # One completes, one strands, one is still driving. If the active one
        # were counted as an exit the network would appear to clear.
        trips = [trip(0, 100, True), trip(0, 200, False, stranded=True),
                 trip(0, 9999, False)]
        doc = mkflowcurve.build(metrics(1000, trips), 30.0)
        self.assertEqual(doc["totals"]["injected"], 3)
        self.assertEqual(doc["totals"]["completed"], 1)
        self.assertEqual(doc["totals"]["stranded"], 1)
        self.assertEqual(doc["totals"]["activeAtHorizon"], 1)
        last = doc["bins"][-1]
        self.assertEqual(last["cumDone"], 1)
        self.assertEqual(last["cumStrand"], 1)
        self.assertEqual(last["inNet"], 1)

    def test_the_accumulation_identity_holds_in_every_bin(self):
        trips = [trip(i * 10, i * 10 + 500, i % 3 != 0) for i in range(40)]
        doc = mkflowcurve.build(metrics(1000, trips), 10.0)
        for b in doc["bins"]:
            self.assertEqual(
                b["inNet"], b["cumArr"] - b["cumDone"] - b["cumStrand"],
                f"bin at tick {b['tick']} breaks the identity")

    def test_it_refuses_when_the_ledger_disagrees_with_totals(self):
        m = metrics(1000, [trip(0, 100, True)])
        m["totals"]["completed_trips"] = 99          # a lie
        with self.assertRaises(SystemExit) as cm:
            mkflowcurve.build(m, 30.0)
        self.assertIn("completed", str(cm.exception))


class TestEmptyRun(unittest.TestCase):
    def test_a_run_with_no_trips_does_not_raise(self):
        # Legal: a horizon too short for the first arrival, or no demand.
        # max() over an empty ledger raised a bare ValueError.
        doc = mkflowcurve.build(metrics(1000, []), 30.0)
        self.assertEqual(doc["lastEntryTick"], 0)
        self.assertEqual(doc["totals"]["injected"], 0)
        self.assertEqual(doc["totals"]["peakInNet"], 0)
        self.assertTrue(all(b["inNet"] == 0 for b in doc["bins"]))


if __name__ == "__main__":
    unittest.main()
