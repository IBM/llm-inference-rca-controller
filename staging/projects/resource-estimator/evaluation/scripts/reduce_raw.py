"""
Raw experiment data -> the processed per-run tables that gpu_figures.py reads.

Every run is reduced to one row over one steady window (Unix time, from the vLLM series):
  start = first sample with num_requests_running > 0, plus the run's mean E2E latency
          (ramp-up: every request in flight arrived after the start)
  end   = first sample where prompt_tokens_total reaches its final value (the drain follows)
DCGM samples are taken from [start + DCGM_DELAY_S, end].  A run whose window is shorter than
DCGM_DELAY_S + 30 s gets status `no_window` and no values.  vLLM gauges are window means,
histograms (ITL, prefill time) are d sum / d count over the window, DCGM TENSOR_ACTIVE / SM_ACTIVE
are given as the window mean (*_mean) and maximum (*_max).  The tables hold measured quantities
only; the estimates are evaluated by gpu_figures.py.

An open-loop run is `overloaded` when on average at least one request waits in vLLM's queue over
the window (vllm:num_requests_waiting mean >= 1).

Layout of an experiment (as written by the acquisition scripts):
  raw/sweep_<group>/<point>.metrics.json   Prometheus range query (vLLM + DCGM)
  raw/sweep_<group>/<point>.extra.json     further vLLM series (waiting, preemptions)
  raw/<group>/BENCH--<point>/run=0[.slim].json   vllm bench serve result
<point> is in<I>_out<O>.  The disaggregated runs have <point>.prefill.* and <point>.decode.* files.

Where the raw data is looked for, in this order, per dataset of a group (single-gpu | disaggregated):
  --raw-root/<dataset>/raw
  evaluation/data/gpu/sources/<group>/<dataset>/raw

Usage (from the repository root):
  python evaluation/scripts/reduce_raw.py single-gpu [dataset ...]      -> data/gpu/processed/single-gpu/<dataset>.csv
  python evaluation/scripts/reduce_raw.py disaggregated [dataset ...]   -> data/gpu/processed/disaggregated/<dataset>.csv
  python evaluation/scripts/reduce_raw.py list                          the datasets of both groups and where their raw data is
  no dataset: every one of the group that has raw data
  options: --raw-root DIR   --out DIR (default: evaluation/data/gpu/processed)
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from dataclasses import dataclass
from typing import Iterator

import numpy as np
import pandas as pd

EVALUATION = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
SOURCES = os.path.join(EVALUATION, "data", "gpu", "sources")
PROCESSED = os.path.join(EVALUATION, "data", "gpu", "processed")

DCGM_DELAY_S = 90.0  # DCGM reports 30 s blocks that lag vLLM by up to 60 s
OVERLOADED_WAITING = 1.0  # mean vllm:num_requests_waiting from which an open-loop run is overloaded


@dataclass(frozen=True)
class Dataset:
    name: str  # the experiment directory
    model: str
    group_pattern: str  # regex on the raw/ directory names (sweep_ prefix included), with named groups


DATASETS = {d.name: d for d in [
    Dataset("granite_mps_input_rate16_20260929", "granite-4.1-3b",
            r"sweep_(?P<sweep>input)_len_granite3b_mpsin_\d+_poisson_rate(?P<rate>[\d.]+)_mps(?P<mps>\d+)"),
    Dataset("granite_mps_input_rate2_20261001", "granite-4.1-3b",
            r"sweep_(?P<sweep>input)_len_granite3b_mpsin_\d+_poisson_rate(?P<rate>[\d.]+)_mps(?P<mps>\d+)"),
    Dataset("granite_rec_20261006", "granite-4.1-3b",
            r"sweep_(?P<sweep>input)_len_granite3b_rec_\d+_poisson_rate(?P<rate>[\d.]+)_in\d+_mps(?P<mps>\d+)"),
    Dataset("granite_mps_output_20261015", "granite-4.1-3b",
            r"sweep_(?P<sweep>output)_len_granite3b_mpsout_\d+_poisson_rate(?P<rate>[\d.]+)_out\d+_mps(?P<mps>\d+)"),
    Dataset("qwen3_8b_mps_input_rate16_20261008", "qwen3-8b",
            r"sweep_(?P<sweep>input)_len_qwen3_8b_mpsin_\d+_poisson_rate(?P<rate>[\d.]+)_in\d+_mps(?P<mps>\d+)"),
    Dataset("qwen3_8b_mps_input_rate2_20261008", "qwen3-8b",
            r"sweep_(?P<sweep>input)_len_qwen3_8b_mpsin_\d+_poisson_rate(?P<rate>[\d.]+)_in\d+_mps(?P<mps>\d+)"),
    Dataset("qwen3_8b_rec_20261009", "qwen3-8b",
            r"sweep_(?P<sweep>input)_len_qwen3_8b_rec_\d+_poisson_rate(?P<rate>[\d.]+)_in\d+_mps(?P<mps>\d+)"),
    Dataset("qwen3_8b_mps_output_20261015", "qwen3-8b",
            r"sweep_(?P<sweep>output)_len_qwen3_8b_mpsout_\d+_poisson_rate(?P<rate>[\d.]+)_out\d+_mps(?P<mps>\d+)"),
]}

# the disaggregated datasets: Llama-3.1-8B, one prefill and one decode instance, no MPS daemon
DISAGG_DATASETS = ("llama_disagg_20260923",)
DISAGG_GROUP = re.compile(
    r"sweep_(?P<sweep>input|output)_len_disagg_(?P<date>\d+)_(?P<cond>steady|poisson(?:_rate(?P<rate>[\d.]+))?)")
DISAGG_SHAREGPT = "sweep_sharegpt_disagg_20260923"  # points b<burstiness>_rate<R>, lengths from the ShareGPT dataset


def raw_dir(dataset: str, group: str, raw_root: str | None = None) -> str | None:
    """The raw/ directory of `dataset` of `group` (single-gpu | disaggregated), None where neither place has it."""
    for base in ([raw_root] if raw_root else []) + [os.path.join(SOURCES, group)]:
        path = os.path.join(base, dataset, "raw")
        if os.path.isdir(path):
            return path
    return None


# ---------------------------------------------------------------------------
# one run


def load_json(path: str) -> dict:
    with open(path) as fh:
        return json.load(fh)


def series(metrics: dict, name: str) -> tuple[np.ndarray, np.ndarray]:
    values = np.array(metrics[name]["data"]["result"][0]["values"], dtype=float)
    return values[:, 0], values[:, 1]


class Window:
    """The steady window of one run (or of one role of a disaggregated point) and what is read in it.
    `metrics` is searched after `extra` for the series of `extra`."""

    def __init__(self, metrics: dict, extra: dict, bench: dict):
        self.metrics, self.extra = metrics, extra
        self.t_run, self.nrun = series(metrics, "vllm:num_requests_running")
        t_prompt, prompt = series(metrics, "vllm:prompt_tokens_total")
        self.start = self.t_run[np.argmax(self.nrun > 0)] + bench["mean_e2el_ms"] / 1000
        self.end = t_prompt[np.argmax(prompt >= prompt.max())]
        self.length = self.end - self.start
        self.dcgm_start = self.start + DCGM_DELAY_S

    def series(self, name: str) -> tuple[np.ndarray, np.ndarray]:
        return series(self.extra if name in self.extra else self.metrics, name)

    def values(self, name: str, lo: float | None = None) -> np.ndarray:
        t, v = self.series(name)
        return v[(t >= (self.start if lo is None else lo)) & (t <= self.end)]

    def delta(self, name: str) -> float:
        t, v = self.series(name)
        return float(np.interp(self.end, t, v) - np.interp(self.start, t, v))

    def histogram_mean(self, name: str) -> float:
        return self.delta(f"{name}_sum") / self.delta(f"{name}_count")

    def delta_all_labels(self, name: str) -> float:
        """delta() summed over every label series (delta() reads only the first)."""
        total = 0.0
        for s in (self.extra if name in self.extra else self.metrics)[name]["data"]["result"]:
            v = np.array(s["values"], dtype=float)
            total += float(np.interp(self.end, v[:, 0], v[:, 1]) - np.interp(self.start, v[:, 0], v[:, 1]))
        return total

    def mean_in_window(self) -> tuple[float, float]:
        """Mean and maximum of num_requests_running over the window."""
        n = self.nrun[(self.t_run >= self.start) & (self.t_run <= self.end)]
        return float(n.mean()), float(n.max())


def reduce_run(metrics_path: str, extra_path: str, bench_path: str, input_tokens: int | None,
               output_tokens: int | None) -> dict:
    """One row of one run.  Lengths None (ShareGPT): the window averages."""
    metrics = load_json(metrics_path)["metrics"]
    extra = load_json(extra_path)["metrics"] if os.path.exists(extra_path) else {}
    bench = load_json(bench_path)
    w = Window(metrics, extra, bench)
    row = {"offered_rate": float(bench["request_rate"]),  # "inf" for closed loop
           "burstiness": bench["burstiness"], "achieved_rate": bench["request_throughput"],
           "bench_mean_itl_ms": bench["mean_itl_ms"], "window_s": w.length}
    if w.length < DCGM_DELAY_S + 30:
        return {**row, "status": "no_window"}
    if input_tokens is None:
        input_tokens = w.delta("vllm:prompt_tokens_total") / w.delta("vllm:request_prompt_tokens_count")
    if output_tokens is None:
        output_tokens = w.histogram_mean("vllm:request_generation_tokens")
    nrun_mean, nrun_max = w.mean_in_window()
    tensor = w.values("DCGM_FI_PROF_PIPE_TENSOR_ACTIVE", w.dcgm_start) * 100
    sm = w.values("DCGM_FI_PROF_SM_ACTIVE", w.dcgm_start) * 100
    cache = metrics["vllm:cache_config_info"]["data"]["result"][0]["metric"]
    row.update({
        "input_tokens": input_tokens, "output_tokens": output_tokens,
        "tensor_mean": float(tensor.mean()), "tensor_max": float(tensor.max()),
        "sm_mean": float(sm.mean()), "sm_max": float(sm.max()),
        "fb_used_mib": float(w.values("DCGM_FI_DEV_FB_USED", w.dcgm_start).max()),  # flat: vLLM preallocates its KV pool
        "nrun_mean": nrun_mean, "nrun_max": nrun_max,
        # completions per second, every finished_reason; vLLM counts no arrivals
        "arrival_rate": w.delta_all_labels("vllm:request_success_total") / w.length,
        "itl_ms": w.histogram_mean("vllm:inter_token_latency_seconds") * 1000,
        "prefill_time_ms": w.histogram_mean("vllm:request_prefill_time_seconds") * 1000,
        "waiting_mean": float(w.values("vllm:num_requests_waiting").mean()) if "vllm:num_requests_waiting" in extra else np.nan,
        "preemptions": w.delta("vllm:num_preemptions_total") if "vllm:num_preemptions_total" in extra else np.nan,
        "kv_max_pct": float(w.values("vllm:kv_cache_usage_perc").max()) * 100,
        "kv_pool_tokens": int(cache["kv_cache_size_tokens"]),
        "gpu_memory_utilization": float(cache["gpu_memory_utilization"]),
        "dcgm_window_s": w.end - w.dcgm_start,
        "status": "ok",
    })
    return row


def lengths(point: str) -> tuple[int | None, int | None]:
    m = re.fullmatch(r"in(\d+)_out(\d+)", point)
    return (int(m[1]), int(m[2])) if m else (None, None)


# ---------------------------------------------------------------------------
# single GPU


def runs(dataset: Dataset, raw: str) -> Iterator[dict]:
    """The label columns and the paths of every run of `dataset` found under `raw`."""
    for group in sorted(os.listdir(raw)):
        m = re.fullmatch(dataset.group_pattern, group)
        if not m:
            continue
        g = m.groupdict()
        cond = f"poisson_rate{g['rate']}"
        bench_root = os.path.join(raw, group.removeprefix("sweep_"))
        for f in sorted(os.listdir(os.path.join(raw, group))):
            if not f.endswith(".metrics.json") or f.startswith("_"):
                continue
            point = f.removesuffix(".metrics.json")
            bench_dir = os.path.join(bench_root, f"BENCH--{point}")
            bench = next((os.path.join(bench_dir, b) for b in ("run=0.slim.json", "run=0.json")
                          if os.path.exists(os.path.join(bench_dir, b))), None)
            if bench is None:
                continue
            i, o = lengths(point)
            yield {
                "labels": {"dataset": dataset.name, "model": dataset.model, "group": group, "point": point,
                           "sweep": g["sweep"], "arrival": "poisson", "condition": cond,
                           "mps_pct": int(g["mps"]) if g.get("mps") else None, "concurrency": None,
                           "target_input_tokens": i, "target_output_tokens": o},
                "metrics": os.path.join(raw, group, f),
                "extra": os.path.join(raw, group, f"{point}.extra.json"),
                "bench": bench,
            }


def arrival_tag(row: pd.Series) -> str:
    return f"poisson-rate{row.offered_rate:g}"


def reduce_single_gpu(dataset: Dataset, raw: str) -> pd.DataFrame:
    df = pd.DataFrame([{**r["labels"], **reduce_run(r["metrics"], r["extra"], r["bench"],
                                                    r["labels"]["target_input_tokens"],
                                                    r["labels"]["target_output_tokens"])}
                       for r in runs(dataset, raw)])
    judged = df.status == "ok"
    df["overloaded"] = pd.Series(pd.NA, index=df.index, dtype="boolean")
    df.loc[judged, "overloaded"] = (df.waiting_mean >= OVERLOADED_WAITING)[judged]
    df["arrival_tag"] = df.apply(arrival_tag, axis=1)
    df["mps_tag"] = [f"mps{100 if pd.isna(p) else int(p)}" for p in df.mps_pct]  # no MPS daemon: labelled mps100
    return df


# ---------------------------------------------------------------------------
# disaggregated Llama-3.1-8B: one prefill and one decode instance, the window from the decode one


def role_files(raw: str, group: str, point: str, role: str) -> tuple[dict, dict]:
    base = os.path.join(raw, group, f"{point}.{role}")
    return load_json(base + ".metrics.json")["metrics"], load_json(base + ".extra.json")["metrics"]


def reduce_disagg_point(raw: str, group: str, point: str, bench_path: str, input_tokens: int | None,
                        output_tokens: int | None) -> dict:
    """Lengths None (ShareGPT): the window averages of the decode instance."""
    bench = load_json(bench_path)
    roles = {r: Window(*role_files(raw, group, point, r), bench) for r in ("prefill", "decode")}
    d = roles["decode"]
    row = {"offered_rate": float(bench["request_rate"]), "burstiness": bench["burstiness"],
           "achieved_rate": bench["request_throughput"], "bench_mean_ttft_ms": bench["mean_ttft_ms"],
           "bench_mean_itl_ms": bench["mean_itl_ms"], "window_s": d.length}
    if d.length < DCGM_DELAY_S + 30:
        return {**row, "status": "no_window"}
    for w in roles.values():  # both roles are read in the window of the decode instance
        w.start, w.end, w.dcgm_start = d.start, d.end, d.dcgm_start
    if input_tokens is None:
        input_tokens = d.delta("vllm:prompt_tokens_total") / d.delta("vllm:request_prompt_tokens_count")
        output_tokens = d.histogram_mean("vllm:request_generation_tokens")
    row.update(input_tokens=input_tokens, output_tokens=output_tokens)
    for role, w in roles.items():
        for key, name in (("tensor", "DCGM_FI_PROF_PIPE_TENSOR_ACTIVE"), ("sm", "DCGM_FI_PROF_SM_ACTIVE"),
                          ("dram", "DCGM_FI_PROF_DRAM_ACTIVE")):
            v = w.values(name, w.dcgm_start) * 100
            row[f"{role}_{key}_mean"], row[f"{role}_{key}_max"] = float(v.mean()), float(v.max())
    for role, w in roles.items():  # memory inputs per role
        cache = w.metrics["vllm:cache_config_info"]["data"]["result"][0]["metric"]
        row[f"{role}_kv_pool_tokens"] = int(cache["kv_cache_size_tokens"])
        row[f"{role}_kv_max_pct"] = float(w.values("vllm:kv_cache_usage_perc").max()) * 100
        row[f"{role}_fb_used_mib"] = float(w.values("DCGM_FI_DEV_FB_USED", w.dcgm_start).max())
    p = roles["prefill"]
    queries, hits = p.delta("vllm:prefix_cache_queries_total"), p.delta("vllm:prefix_cache_hits_total")
    n = d.values("vllm:num_requests_running")
    row.update({
        "arrival_rate": d.delta_all_labels("vllm:request_success_total") / d.length,
        "prefill_cache_hit": hits / queries if queries else 0.0,
        "prefill_time_ms": p.histogram_mean("vllm:request_prefill_time_seconds") * 1000,
        "itl_ms": d.histogram_mean("vllm:inter_token_latency_seconds") * 1000,
        "nrun_mean": float(n.mean()), "nrun_max": float(n.max()),
        "waiting_mean": float(d.values("vllm:num_requests_waiting").mean()),  # the queue is at the decode instance
        "status": "ok",
    })
    return row


def reduce_disaggregated(raw: str) -> pd.DataFrame:
    rows = []
    for group in sorted(os.listdir(raw)):
        m = DISAGG_GROUP.fullmatch(group)
        if not m and group != DISAGG_SHAREGPT:
            continue
        bench_root = os.path.join(raw, group.removeprefix("sweep_"))
        for f in sorted(os.listdir(os.path.join(raw, group))):
            if not f.endswith(".prefill.metrics.json") or f.startswith("_"):
                continue
            point = f.removesuffix(".prefill.metrics.json")
            bench = os.path.join(bench_root, f"BENCH--{point}", "run=0.json")
            i, o = (int(x) for x in re.fullmatch(r"in(\d+)_out(\d+)", point).groups()) if m else (None, None)
            rows.append({"group": group, "sweep": m["sweep"] if m else "sharegpt",
                         "condition": m["cond"] if m else "sharegpt", "point": point,
                         **reduce_disagg_point(raw, group, point, bench, i, o)})
    return pd.DataFrame(rows)


# ---------------------------------------------------------------------------


def write(df: pd.DataFrame, path: str) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    df.to_csv(path, index=False)
    print(f"{path}: {len(df)} runs, status {df.status.value_counts().to_dict()}")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("command", choices=("single-gpu", "disaggregated", "list"))
    ap.add_argument("datasets", nargs="*", help="the datasets to reduce (default: every one with raw data)")
    ap.add_argument("--raw-root", help="a directory of <dataset>/raw, searched before the default places")
    ap.add_argument("--out", default=PROCESSED, help="the processed directory (default: evaluation/data/gpu/processed)")
    args = ap.parse_args()

    if args.command == "list":
        for group, datasets in (("single-gpu", DATASETS), ("disaggregated", DISAGG_DATASETS)):
            for name in datasets:
                print(f"{group:14s} {name:38s} {raw_dir(name, group, args.raw_root) or '(no raw data found)'}")
        return

    group = args.command
    known = DATASETS if group == "single-gpu" else DISAGG_DATASETS
    unknown = [n for n in args.datasets if n not in known]
    if unknown:
        sys.exit(f"unknown {group} dataset {', '.join(unknown)}; the datasets are {', '.join(known)}")
    for name in args.datasets or known:
        raw = raw_dir(name, group, args.raw_root)
        if raw is None:
            if args.datasets:
                sys.exit(f"no raw data for {name}")
            print(f"{name}: no raw data found, skipped")
            continue
        df = reduce_single_gpu(DATASETS[name], raw) if group == "single-gpu" else reduce_disaggregated(raw)
        write(df, os.path.join(args.out, group, f"{name}.csv"))


if __name__ == "__main__":
    main()
