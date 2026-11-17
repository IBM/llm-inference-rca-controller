# Evaluation

Scripts and data that check the GPU estimator against measured
vLLM runs on an A100-80GB. The scripts run in this order:

```
vLLM servers ──run_workload.py──▶ raw measurements ──reduce_raw.py──▶ processed per-run CSVs
 (mps.sh)
```

## Setup

`run_workload.py` uses only the Python standard library. `reduce_raw.py` needs `numpy` and `pandas`:

```bash
python3 -m venv .venv && .venv/bin/pip install numpy pandas
```

## Layout

```
evaluation/
  scripts/
    run_workload.py      running vLLM servers -> raw data
    mps.sh               start / stop an MPS daemon with an SM cap
    reduce_raw.py        raw data -> processed CSVs
  data/gpu/
    sources/             raw data of one experiment per directory: raw/, logs/, scripts/ (the acquisition scripts)
      single-gpu/<dataset>/                 one vLLM instance (granite, qwen3-8b)
      disaggregated/<dataset>/              a prefill and a decode instance (llama_disagg_20260923)
    processed/
      single-gpu/<dataset>.csv                one row per run
      disaggregated/llama_disagg_20260923.csv one row per prefill/decode point
```

## `scripts/run_workload.py`: running servers to raw data

Runs one sweep (one `vllm bench serve` per point) against vLLM servers that are **already running**, and scrapes the
`/metrics` endpoints of every vLLM instance and of dcgm-exporter directly, each `--step` seconds (default 5), until
`--pad-after` seconds (default 45) after the point ends because DCGM lags the load. No Prometheus is needed. The samples
are written in the shape `reduce_raw.py` reads. It needs `vllm` on `PATH`, `nvidia-smi`, and a dcgm-exporter
(default `http://127.0.0.1:9400/metrics`) reporting `DCGM_FI_PROF_*`.

Single GPU and disaggregated runs use the same call: one `--instance [ROLE=]URL@GPU` per vLLM server, where `GPU` is the
CUDA index on this host (mapped to dcgm-exporter's own `gpu` label through the GPU UUID).

```bash
# single GPU: input-length sweep at Poisson 2 req/s
python evaluation/scripts/run_workload.py --group single-gpu --dataset granite_rec_20261006 \
    --run-name granite3b_rec_20261006_poisson_rate2_in512_mps50 --model ibm-granite/granite-4.1-3b \
    --instance http://127.0.0.1:8001@0 --sweep input --values 64 256 1024 --rate 2 --num-prompts 600

# disaggregated: a prefill and a decode instance, the benchmark goes through the proxy
python evaluation/scripts/run_workload.py --group disaggregated --dataset llama_disagg_20260923 \
    --run-name disagg_20260923_poisson_rate2 --model meta-llama/Llama-3.1-8B-Instruct \
    --instance prefill=http://127.0.0.1:8100@0 --instance decode=http://127.0.0.1:8200@1 \
    --base-url http://127.0.0.1:8000 --sweep output --values 32 128 512 --rate 2 --num-prompts 600
```

Output goes to `data/gpu/sources/<group>/<dataset>/raw`, or to `--out DIR`. The sweep is `input` / `output` (`--values`
are the lengths, `--rate` the request rate) or `sharegpt` (`--values` are request rates, `--dataset-path` the ShareGPT
file). `--run-name` must make the directory name match the dataset's pattern in `reduce_raw.py`. An existing run
directory is never overwritten. Between points the prefix caches are reset (this needs `VLLM_SERVER_DEV_MODE=1` on the
server; a failed reset is a warning) and the script idles for `--idle` seconds (default 90). Run `--help` for all options.

### `scripts/mps.sh`: MPS setting

MPS is not part of the workload script. Start the daemon, start the server from that shell, then run the workload:

```bash
eval "$(evaluation/scripts/mps.sh start 0 50)"   # GPU 0, SM cap 50 %; exports the MPS environment
vllm serve ibm-granite/granite-4.1-3b --max-num-seqs 512 --port 8001 &
python evaluation/scripts/run_workload.py ...
evaluation/scripts/mps.sh stop
```

The SM cap is fixed when the server attaches: to change it, stop the server, run `start` again and restart the server. The
GPU must have no other CUDA context. `MPS_DIR` (default `$TMPDIR/mps-<uid>`) holds the pipe and log directories.

## `scripts/reduce_raw.py`: raw data to CSV

Reduces every run to one row over one steady window: the start is the first sample with running requests plus
the mean E2E latency, the end is when the last prompt has been prefilled, and DCGM is read from 90 s after the start.
A run with a window under 120 s gets `status = no_window`. The tables hold measured quantities only: ITL, prefill
time, DCGM TENSOR_ACTIVE / SM_ACTIVE / DRAM_ACTIVE, framebuffer use, KV pool size and peak use, arrival rate,
queue length. An open-loop run is `overloaded` when on average at least one request waits in vLLM's queue.

```bash
python evaluation/scripts/reduce_raw.py list                                      # datasets of both groups and where their raw data is
python evaluation/scripts/reduce_raw.py single-gpu [dataset ...]                  # single GPU group
python evaluation/scripts/reduce_raw.py disaggregated [dataset ...]               # disaggregated group
```

Without a dataset, every dataset of the group that has raw data is reduced. Examples:

```bash
python evaluation/scripts/reduce_raw.py single-gpu qwen3_8b_rec
python evaluation/scripts/reduce_raw.py disaggregated llama_disagg
```

Each writes `data/gpu/processed/<group>/<dataset>.csv`.

| option | meaning |
|---|---|
| `--raw-root DIR` | a directory of `<dataset>/raw`, searched first |
| `--out DIR` | the processed directory; default `evaluation/data/gpu/processed` |

Raw data is looked for in this order: `--raw-root/<dataset>/raw`, then
`evaluation/data/gpu/sources/<group>/<dataset>/raw`. A dataset with no raw data is skipped when none is named, and an error when named.

Raw layout of an experiment (as the acquisition scripts write it):

```
raw/sweep_<group>/<point>.metrics.json      Prometheus range query (vLLM + DCGM)
raw/sweep_<group>/<point>.extra.json        waiting requests, preemptions
raw/<group>/BENCH--<point>/run=0[.slim].json  vllm bench serve result
```

`<point>` is `in<I>_out<O>`. Disaggregated points have `<point>.prefill.*` and `<point>.decode.*` files, and
the window comes from the decode instance.
