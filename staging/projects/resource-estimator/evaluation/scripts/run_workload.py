#!/usr/bin/env python3
"""
Run one workload sweep against vLLM servers that are ALREADY running, and write the raw data that
reduce_raw.py reads.  Start the servers (and, if wanted, the MPS daemon: evaluation/scripts/mps.sh) first.

Per point of the sweep, `vllm bench serve` is run once while the /metrics endpoints are scraped directly:
  * every vLLM instance: http://<host>:<port>/metrics
  * dcgm-exporter:       --dcgm-url (default http://127.0.0.1:9400/metrics), the series of the instance's GPU
No Prometheus is needed.  The scraped samples are written in the shape of a Prometheus range query, so
reduce_raw.py reads them unchanged.  Scraping goes on for --pad-after seconds after the benchmark ends, because
DCGM lags the load.

Single GPU and disaggregated runs use the same pattern: one --instance per vLLM server.
  single-gpu     one instance, files are <point>.metrics.json
  disaggregated  a prefill and a decode instance, files are <point>.<role>.metrics.json;
                 --base-url is the proxy the benchmark talks to

  --instance [ROLE=]URL@GPU      URL of the vLLM server, GPU its CUDA device index on this host (as nvidia-smi
                                 numbers it); it is mapped to the label dcgm-exporter uses through the GPU's UUID.
                                 ROLE is prefill or decode for the disaggregated group.

Output (default evaluation/data/gpu/sources/<group>/<dataset>/raw, or --out):
  sweep_<prefix><run-name>/<point>[.<role>].metrics.json   scraped vLLM + DCGM series
  sweep_<prefix><run-name>/<point>[.<role>].extra.json     waiting requests, preemptions, token sums
  sweep_<prefix><run-name>/<point>.window.json             start / end of the benchmark
  <prefix><run-name>/BENCH--<point>/run=0.json             vllm bench serve result
<prefix> is input_len_ / output_len_ / sharegpt_ for --sweep input / output / sharegpt.  Choose --run-name so
that the whole directory name matches the dataset's pattern in reduce_raw.py.

Examples (from the repository root):
  # single GPU: input-length sweep, Poisson 2 req/s
  python evaluation/scripts/run_workload.py --group single-gpu --dataset granite_rec_20261006 \\
      --run-name granite3b_rec_20261006_poisson_rate2_in512_mps50 --model ibm-granite/granite-4.1-3b \\
      --instance http://127.0.0.1:8001@0 --sweep input --values 64 256 1024 --rate 2 --num-prompts 600

  # disaggregated: output-length sweep through the proxy
  python evaluation/scripts/run_workload.py --group disaggregated --dataset llama_disagg_20260923 \\
      --run-name disagg_20260923_poisson_rate2 --model meta-llama/Llama-3.1-8B-Instruct \\
      --instance prefill=http://127.0.0.1:8100@0 --instance decode=http://127.0.0.1:8200@1 \\
      --base-url http://127.0.0.1:8000 --sweep output --values 32 128 512 --rate 2 --num-prompts 600
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field

EVALUATION = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
SOURCES = os.path.join(EVALUATION, "data", "gpu", "sources")

GROUPS = ("single-gpu", "disaggregated")
ROLES = ("prefill", "decode")
HTTP_TIMEOUT_S = 10
WARMUP = "_warmup"  # a throwaway burst before the points; reduce_raw.py skips names starting with _

DCGM_METRICS = [
    "DCGM_FI_PROF_SM_ACTIVE", "DCGM_FI_PROF_SM_OCCUPANCY", "DCGM_FI_PROF_DRAM_ACTIVE",
    "DCGM_FI_PROF_PIPE_TENSOR_ACTIVE", "DCGM_FI_DEV_FB_USED", "DCGM_FI_DEV_FB_TOTAL",
]
VLLM_METRICS = [
    "vllm:num_requests_running", "vllm:kv_cache_usage_perc", "vllm:cache_config_info",
    "vllm:prefix_cache_queries_total", "vllm:prefix_cache_hits_total",
    "vllm:prompt_tokens_total", "vllm:generation_tokens_total", "vllm:request_success_total",
    "vllm:request_prompt_tokens_count",
    "vllm:time_to_first_token_seconds_count", "vllm:time_to_first_token_seconds_sum",
    "vllm:inter_token_latency_seconds_count", "vllm:inter_token_latency_seconds_sum",
    "vllm:e2e_request_latency_seconds_count",
    "vllm:request_prefill_time_seconds_count", "vllm:request_prefill_time_seconds_sum",
    "vllm:request_decode_time_seconds_count", "vllm:request_decode_time_seconds_sum",
]
EXTRA_METRICS = [  # a second file, as the Prometheus fetch of the earlier runs wrote it
    "vllm:num_requests_waiting", "vllm:num_preemptions_total", "vllm:request_prompt_tokens_sum",
    "vllm:request_generation_tokens_sum", "vllm:request_generation_tokens_count",
]
WANTED = set(DCGM_METRICS) | set(VLLM_METRICS) | set(EXTRA_METRICS)

SAMPLE = re.compile(r"^([A-Za-z_:][A-Za-z0-9_:]*)(?:\{(.*)\})?\s+(\S+)")
LABEL = re.compile(r'([A-Za-z_][A-Za-z0-9_]*)="((?:[^"\\]|\\.)*)"')


def log(msg: str) -> None:
    print(f"[run_workload] {msg}", file=sys.stderr, flush=True)


# ---------------------------------------------------------------------------
# /metrics scraping


def parse_exposition(text: str):
    """(name, labels, value text) of every sample line of a Prometheus text exposition."""
    for line in text.splitlines():
        if line.startswith("#"):
            continue
        m = SAMPLE.match(line)
        if m:
            yield m[1], dict(LABEL.findall(m[2] or "")), m[3]


def fetch(url: str) -> str:
    with urllib.request.urlopen(url, timeout=HTTP_TIMEOUT_S) as resp:
        return resp.read().decode()


@dataclass
class Instance:
    role: str  # "" for single-gpu
    url: str
    cuda_index: str
    dcgm_label: str = ""


def resolve_dcgm_label(cuda_index: str, dcgm_url: str) -> str:
    """The `gpu` label dcgm-exporter gives the GPU, found through its UUID (DCGM numbers GPUs on its own)."""
    uuid = subprocess.run(["nvidia-smi", "-i", cuda_index, "--query-gpu=uuid", "--format=csv,noheader"],
                          check=True, capture_output=True, text=True, timeout=HTTP_TIMEOUT_S).stdout.strip()
    for name, labels, _ in parse_exposition(fetch(dcgm_url)):
        if name == "DCGM_FI_PROF_SM_ACTIVE" and labels.get("UUID") == uuid:
            return labels["gpu"]
    raise SystemExit(f"error: DCGM_FI_PROF_SM_ACTIVE of GPU {cuda_index} ({uuid}) not found at {dcgm_url}")


class Scraper:
    """Samples every instance's /metrics and the DCGM page each `step` seconds, between begin() and finish()."""

    def __init__(self, instances: list[Instance], dcgm_url: str, step: float):
        self.instances, self.dcgm_url, self.step = instances, dcgm_url, step
        self.series: dict = {}
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self.first = self.last = 0.0

    def begin(self) -> None:
        self.series = {i.role: {} for i in self.instances}
        self._stop.clear()
        self.first = time.time()
        self._thread = threading.Thread(target=self._loop, daemon=True)
        self._thread.start()

    def finish(self) -> dict:
        self._stop.set()
        self._thread.join()
        return self.series

    def _loop(self) -> None:
        next_tick = time.monotonic()
        while not self._stop.is_set():
            self._tick()
            next_tick += self.step
            self._stop.wait(max(0.0, next_tick - time.monotonic()))

    def _tick(self) -> None:
        t = time.time()
        self.last = t
        try:
            dcgm = list(parse_exposition(fetch(self.dcgm_url)))
        except (OSError, ValueError) as e:
            log(f"warning: DCGM scrape failed: {e}")
            dcgm = []
        for inst in self.instances:
            try:
                vllm = list(parse_exposition(fetch(inst.url + "/metrics")))
            except (OSError, ValueError) as e:
                log(f"warning: scrape of {inst.url} failed: {e}")
                continue
            by_name = self.series[inst.role]
            for name, labels, value in vllm + [s for s in dcgm if s[1].get("gpu") == inst.dcgm_label]:
                if name in WANTED:
                    key = tuple(sorted(labels.items()))
                    by_name.setdefault(name, {}).setdefault(key, {"metric": {"__name__": name, **labels},
                                                                  "values": []})["values"].append([t, value])


def matrix(by_name: dict, names: list[str]) -> dict:
    """The samples of `names` as the answer of a Prometheus query_range."""
    return {n: {"status": "success",
                "data": {"resultType": "matrix", "result": list(by_name.get(n, {}).values())}} for n in names}


# ---------------------------------------------------------------------------
# the sweep


@dataclass
class Point:
    name: str
    bench_args: list[str] = field(default_factory=list)


def random_args(input_len: int, output_len: int) -> list[str]:
    return ["--dataset-name", "random", "--random-input-len", str(input_len),
            "--random-output-len", str(output_len), "--random-range-ratio", "0"]


def build_points(args: argparse.Namespace) -> list[Point]:
    paced = ["--burstiness", f"{args.burstiness:g}"]
    if args.max_concurrency:
        paced += ["--max-concurrency", str(args.max_concurrency)]
    points = []
    for v in args.values:
        if args.sweep == "sharegpt":
            points.append(Point(f"b{args.burstiness:g}_rate{v:g}",
                                ["--dataset-name", "sharegpt", "--dataset-path", args.dataset_path,
                                 "--request-rate", f"{v:g}"] + paced))
            continue
        i, o = (int(v), args.output_len) if args.sweep == "input" else (args.input_len, int(v))
        points.append(Point(f"in{i}_out{o}", random_args(i, o) + ["--request-rate", f"{args.rate:g}"] + paced))
    for p in points:
        p.bench_args += ["--num-prompts", str(args.num_prompts)]
    return points


def run_bench(args: argparse.Namespace, base_url: str, point: Point, bench_dir: str, window_path: str) -> None:
    """vllm bench serve for one point; the wall-clock window it ran in is saved to window_path."""
    result_dir = os.path.join(bench_dir, f"BENCH--{point.name}")
    os.makedirs(result_dir, exist_ok=True)
    # e2el is not in vllm bench serve's default --percentile-metrics (ttft,tpot,itl), and reduce_raw.py reads
    # mean_e2el_ms from the result
    cmd = ["vllm", "bench", "serve", "--backend", "vllm", "--model", args.model, "--base-url", base_url,
           "--endpoint", "/v1/completions", "--ignore-eos", "--seed", "0", "--save-result", "--save-detailed",
           "--percentile-metrics", "ttft,tpot,itl,e2el",
           "--result-dir", result_dir, "--result-filename", "run=0.json"] + point.bench_args
    log("running: " + " ".join(cmd))
    start = time.time()
    rc = subprocess.run(cmd).returncode
    end = time.time()
    with open(window_path, "w") as fh:
        json.dump({"command": cmd, "start_unix": start, "end_unix": end, "duration_s": end - start,
                   "returncode": rc}, fh, indent=2)
    if rc != 0:
        raise SystemExit(f"error: vllm bench serve exited with code {rc} at point {point.name}")


def write_point(sweep_dir: str, point: str, role: str, by_name: dict, window: dict, scraper: Scraper,
                step: float) -> None:
    stem = os.path.join(sweep_dir, f"{point}.{role}" if role else point)
    common = {"start_unix": window["start_unix"], "end_unix": window["end_unix"],
              "query_start_unix": scraper.first, "query_end_unix": scraper.last, "step": f"{step:g}s"}
    for suffix, names in (("metrics", DCGM_METRICS + VLLM_METRICS), ("extra", EXTRA_METRICS)):
        with open(f"{stem}.{suffix}.json", "w") as fh:
            json.dump({**common, "metrics": matrix(by_name, names)}, fh)


def reset_caches(instances: list[Instance]) -> None:
    for inst in instances:
        for ep in ("reset_prefix_cache", "reset_mm_cache", "reset_encoder_cache"):
            req = urllib.request.Request(f"{inst.url}/{ep}", method="POST")
            try:
                urllib.request.urlopen(req, timeout=HTTP_TIMEOUT_S).read()
            except (OSError, ValueError) as e:  # the endpoints need VLLM_SERVER_DEV_MODE=1 on the server
                log(f"warning: {inst.url}/{ep} failed: {e}")


# ---------------------------------------------------------------------------


def parse_instance(spec: str, group: str) -> Instance:
    m = re.fullmatch(r"(?:(\w+)=)?(https?://[^@]+)@(\d+)", spec)
    if not m:
        raise SystemExit(f"error: --instance {spec!r} is not [ROLE=]URL@GPU")
    role, url, gpu = m[1] or "", m[2].rstrip("/"), m[3]
    if group == "single-gpu" and role:
        raise SystemExit("error: a single-gpu instance takes no ROLE")
    if group == "disaggregated" and role not in ROLES:
        raise SystemExit(f"error: a disaggregated instance needs ROLE prefill or decode, got {spec!r}")
    return Instance(role, url, gpu)


def parse_args() -> argparse.Namespace:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--group", required=True, choices=GROUPS)
    ap.add_argument("--dataset", required=True, help="dataset name: the directory under sources/<group>/")
    ap.add_argument("--run-name", required=True, help="names the sweep directory, see above")
    ap.add_argument("--out", help="the raw directory (default: evaluation/data/gpu/sources/<group>/<dataset>/raw)")
    ap.add_argument("--model", required=True, help="model name the servers serve")
    ap.add_argument("--instance", action="append", required=True, metavar="[ROLE=]URL@GPU")
    ap.add_argument("--base-url", help="where the benchmark sends requests (default: the single instance; "
                                       "required for disaggregated: the proxy)")
    ap.add_argument("--dcgm-url", default="http://127.0.0.1:9400/metrics")
    ap.add_argument("--sweep", required=True, choices=("input", "output", "sharegpt"))
    ap.add_argument("--values", required=True, nargs="+", type=float,
                    help="input lengths, output lengths or (sharegpt) request rates, one point each")
    ap.add_argument("--input-len", type=int, default=512, help="fixed input length of an output sweep")
    ap.add_argument("--output-len", type=int, default=256, help="fixed output length of an input sweep")
    ap.add_argument("--rate", type=float, help="request rate of an input / output sweep (req/s)")
    ap.add_argument("--burstiness", type=float, default=1.0, help="1 = Poisson, 100 ~ steady (default 1)")
    ap.add_argument("--max-concurrency", type=int, help="client-side cap, as for a steady arrival")
    ap.add_argument("--num-prompts", type=int, default=600, help="requests per point (default 600)")
    ap.add_argument("--dataset-path", help="ShareGPT json, for --sweep sharegpt")
    ap.add_argument("--step", type=float, default=5.0, help="seconds between scrapes (default 5)")
    ap.add_argument("--pad-after", type=float, default=45.0,
                    help="seconds to keep scraping after the benchmark ends (default 45)")
    ap.add_argument("--idle", type=float, default=90.0,
                    help="seconds from the end of a point to the start of the next (default 90)")
    ap.add_argument("--no-warmup", action="store_true", help="skip the warm-up burst")
    args = ap.parse_args()

    if args.sweep == "sharegpt":
        if not args.dataset_path:
            ap.error("--sweep sharegpt needs --dataset-path")
    elif args.rate is None:
        ap.error("--sweep input / output needs --rate")
    args.instances = [parse_instance(s, args.group) for s in args.instance]
    roles = sorted(i.role for i in args.instances)
    if args.group == "single-gpu" and len(roles) != 1:
        ap.error("single-gpu takes exactly one --instance")
    if args.group == "disaggregated":
        if roles != sorted(ROLES):
            ap.error("disaggregated takes one prefill and one decode --instance")
        if not args.base_url:
            ap.error("disaggregated needs --base-url (the proxy)")
    args.base_url = (args.base_url or args.instances[0].url).rstrip("/")
    return args


def main() -> None:
    args = parse_args()
    raw = args.out or os.path.join(SOURCES, args.group, args.dataset, "raw")
    prefix = {"input": "input_len_", "output": "output_len_", "sharegpt": "sharegpt_"}[args.sweep]
    bench_dir = os.path.join(raw, prefix + args.run_name)
    sweep_dir = os.path.join(raw, "sweep_" + prefix + args.run_name)
    for d in (bench_dir, sweep_dir):
        if os.path.exists(d):
            raise SystemExit(f"error: {d} already exists, refusing to overwrite")

    for inst in args.instances:  # the servers must be up already
        try:
            fetch(inst.url + "/metrics")
        except (OSError, ValueError) as e:
            raise SystemExit(f"error: no vLLM server at {inst.url}: {e}")
        inst.dcgm_label = resolve_dcgm_label(inst.cuda_index, args.dcgm_url)
        log(f"{inst.role or 'server'}: {inst.url}  CUDA {inst.cuda_index} -> DCGM gpu=\"{inst.dcgm_label}\"")

    os.makedirs(sweep_dir)
    os.makedirs(bench_dir)
    with open(os.path.join(sweep_dir, "_run.json"), "w") as fh:
        json.dump({k: v for k, v in vars(args).items() if k != "instances"}, fh, indent=2)

    points = build_points(args)
    if not args.no_warmup:
        points.insert(0, Point(WARMUP, random_args(64, 8) + ["--num-prompts", "1000"]))
    scraper = Scraper(args.instances, args.dcgm_url, args.step)

    for point in points:
        log(f"=== {time.strftime('%FT%TZ', time.gmtime())} {point.name}")
        window_path = os.path.join(sweep_dir, f"{point.name}.window.json")
        scraper.begin()
        try:
            run_bench(args, args.base_url, point, bench_dir, window_path)
            time.sleep(args.pad_after)  # DCGM lags the load: keep sampling
        finally:
            series = scraper.finish()
        with open(window_path) as fh:
            window = json.load(fh)
        for inst in args.instances:
            write_point(sweep_dir, point.name, inst.role, series[inst.role], window, scraper, args.step)
        reset_caches(args.instances)
        time.sleep(max(0.0, args.idle - args.pad_after))
    log(f"done: {sweep_dir}  {bench_dir}")


if __name__ == "__main__":
    main()
