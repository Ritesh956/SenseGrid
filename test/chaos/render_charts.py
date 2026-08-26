#!/usr/bin/env python3
"""Turns test/chaos/results/*.csv into the P10 report charts (see the
SenseGrid Blueprint, P7/P10): latency vs. fleet size, recovery time by
failure mode, and data loss by failure mode. Reads whatever's actually in
results/ — run ramp.sh/kill_broker.sh/kill_processor.sh/pause_db.sh first,
any subset is fine, this just skips a chart if its CSVs aren't there.

Usage: python render_charts.py [--results-dir DIR] [--out-dir DIR]
Requires: pandas, matplotlib (pip install pandas matplotlib)
"""
import argparse
import glob
import os

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
import pandas as pd


def latest(pattern):
    files = sorted(glob.glob(pattern))
    return files[-1] if files else None


def chart_latency_vs_fleet_size(results_dir, out_dir):
    path = latest(os.path.join(results_dir, "ramp_*.csv"))
    if not path:
        print("skip: latency-vs-fleet-size (no ramp_*.csv found — run ramp.sh)")
        return
    df = pd.read_csv(path)
    df = df.sort_values("fleet_size")

    fig, ax = plt.subplots(figsize=(9, 5.5))
    for col, label in [("e2e_p50_s", "p50"), ("e2e_p95_s", "p95"), ("e2e_p99_s", "p99")]:
        ax.plot(df["fleet_size"], df[col], marker="o", label=f"end-to-end {label}")
    ax.set_xlabel("fleet size (devices)")
    ax.set_ylabel("latency (s)")
    ax.set_title("End-to-end latency vs. fleet size")
    ax.legend()
    ax.grid(True, alpha=0.3)
    fig.tight_layout()
    out = os.path.join(out_dir, "latency_vs_fleet_size.png")
    fig.savefig(out, dpi=150)
    print(f"wrote {out} (source: {path})")

    # Naive saturation-point flag: the fleet size at which p99 first crosses
    # 2x its value at the smallest tested size. This is a starting point for
    # the report's "explained, not just plotted" discussion, not a final
    # verdict — eyeball the curve.
    if len(df) >= 2:
        baseline = df["e2e_p99_s"].iloc[0]
        over = df[df["e2e_p99_s"] > 2 * baseline]
        if not over.empty:
            print(f"note: p99 latency > 2x baseline ({baseline:.3f}s) starting at fleet_size={int(over.iloc[0]['fleet_size'])}")


def chart_recovery_time_by_failure_mode(results_dir, out_dir):
    rows = []
    for pattern, mode, col in [
        # [0-9]* not *: kill_broker.sh also writes a companion
        # kill_broker_seq_gaps_*.csv per-device detail file, which a bare
        # "kill_broker_*.csv" glob also matches (and, sorting after the
        # timestamped result file alphabetically, latest() would pick by
        # mistake — found live). Requiring the char right after the
        # failure-mode prefix to be the timestamp's leading digit excludes
        # it without needing to know every companion file's exact name.
        ("kill_broker_[0-9]*.csv", "broker_restart", "recovery_time_s"),
        ("kill_processor_[0-9]*.csv", "processor_kill", "catchup_time_s"),
        ("pause_db_[0-9]*.csv", "db_pause", "catchup_time_s"),
        ("partition_*.csv", "partition_heal", "convergence_time_s"),
    ]:
        path = latest(os.path.join(results_dir, pattern))
        if not path:
            continue
        df = pd.read_csv(path)
        if col not in df.columns or df.empty:
            continue
        rows.append((mode, float(df[col].iloc[-1])))

    if not rows:
        print("skip: recovery-time-by-failure-mode (no chaos result CSVs found)")
        return

    modes, times = zip(*rows)
    fig, ax = plt.subplots(figsize=(8, 5))
    ax.bar(modes, times, color="#4C72B0")
    ax.set_ylabel("recovery time (s)")
    ax.set_title("Recovery time by failure mode")
    for i, v in enumerate(times):
        ax.text(i, v, f"{v:.1f}s", ha="center", va="bottom")
    fig.tight_layout()
    out = os.path.join(out_dir, "recovery_time_by_failure_mode.png")
    fig.savefig(out, dpi=150)
    print(f"wrote {out}")


def chart_data_loss_by_failure_mode(results_dir, out_dir):
    rows = []
    for pattern, mode in [
        ("kill_broker_[0-9]*.csv", "broker_restart"),
        ("kill_processor_[0-9]*.csv", "processor_kill"),
        ("pause_db_[0-9]*.csv", "db_pause"),
    ]:
        path = latest(os.path.join(results_dir, pattern))
        if not path:
            continue
        df = pd.read_csv(path)
        if "total_seq_gap" not in df.columns or df.empty:
            continue
        rows.append((mode, int(df["total_seq_gap"].iloc[-1])))

    if not rows:
        print("skip: data-loss-by-failure-mode (no chaos result CSVs found)")
        return

    modes, gaps = zip(*rows)
    fig, ax = plt.subplots(figsize=(8, 5))
    colors = ["#55A868" if g == 0 else "#C44E52" for g in gaps]
    ax.bar(modes, gaps, color=colors)
    ax.set_ylabel("missing seq values (sampled devices)")
    ax.set_title("Data loss by failure mode (0 = verified lossless)")
    for i, v in enumerate(gaps):
        ax.text(i, v, str(v), ha="center", va="bottom")
    fig.tight_layout()
    out = os.path.join(out_dir, "data_loss_by_failure_mode.png")
    fig.savefig(out, dpi=150)
    print(f"wrote {out}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--results-dir", default=os.path.join(os.path.dirname(__file__), "results"))
    parser.add_argument("--out-dir", default=os.path.join(os.path.dirname(__file__), "results", "charts"))
    args = parser.parse_args()

    os.makedirs(args.out_dir, exist_ok=True)
    chart_latency_vs_fleet_size(args.results_dir, args.out_dir)
    chart_recovery_time_by_failure_mode(args.results_dir, args.out_dir)
    chart_data_loss_by_failure_mode(args.results_dir, args.out_dir)


if __name__ == "__main__":
    main()
