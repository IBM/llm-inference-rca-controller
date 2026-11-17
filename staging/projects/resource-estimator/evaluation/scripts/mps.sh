#!/bin/bash
# Start / stop an NVIDIA MPS daemon with an SM-fraction cap on one GPU.  Separate from run_workload.py: start MPS
# first, start the vLLM server under it, then run the workload.
#
#   eval "$(evaluation/scripts/mps.sh start <gpu> <pct>)"   # starts the daemon, exports its environment here
#   vllm serve <model> ...                                  # started from this shell: attaches to the daemon
#   evaluation/scripts/mps.sh stop
#   evaluation/scripts/mps.sh status
#
# `start` prints `export` lines on stdout (the pipe / log directories and CUDA_VISIBLE_DEVICES); everything else
# goes to stderr.  A server started in a shell without them does not use MPS.
#
# The SM fraction is fixed when a client attaches: to change <pct>, stop the server, run `start` again and restart
# the server.  MPS attaches only to a GPU with no other CUDA context, so the GPU must be idle (a GPU in
# EXCLUSIVE_PROCESS mode must have no other process on it).
#
# MPS_DIR (default ${TMPDIR:-/tmp}/mps-$(id -u)) holds the pipe and log directories.  Needs nvidia-cuda-mps-control
# on PATH.
set -euo pipefail

MPS_DIR="${MPS_DIR:-${TMPDIR:-/tmp}/mps-$(id -u)}"
export CUDA_MPS_PIPE_DIRECTORY="$MPS_DIR/pipe"
export CUDA_MPS_LOG_DIRECTORY="$MPS_DIR/log"
CTL=nvidia-cuda-mps-control

usage() { echo "usage: $0 start <gpu> <pct> | stop | status" >&2; exit 2; }
[ $# -ge 1 ] || usage
command -v "$CTL" >/dev/null || { echo "ERROR: $CTL not found on PATH" >&2; exit 1; }

daemon_up() { echo get_default_active_thread_percentage | $CTL >/dev/null 2>&1; }

case "$1" in
  start)
    [ $# -eq 3 ] || usage
    GPU="$2"; PCT="$3"
    [[ "$GPU" =~ ^[0-9]+$ ]] || { echo "ERROR: <gpu> must be a device index, got '$GPU'" >&2; exit 2; }
    [[ "$PCT" =~ ^[0-9]+$ ]] && [ "$PCT" -ge 1 ] && [ "$PCT" -le 100 ] \
      || { echo "ERROR: <pct> must be 1..100, got '$PCT'" >&2; exit 2; }

    echo quit | $CTL >/dev/null 2>&1 || true          # drop a stale daemon, if any
    mkdir -p "$CUDA_MPS_PIPE_DIRECTORY" "$CUDA_MPS_LOG_DIRECTORY"
    CUDA_VISIBLE_DEVICES="$GPU" $CTL -d
    sleep 1
    daemon_up || { echo "ERROR: MPS daemon did not start; see $CUDA_MPS_LOG_DIRECTORY" >&2; exit 1; }
    echo "set_default_active_thread_percentage $PCT" | $CTL >/dev/null
    echo "[mps] GPU=$GPU cap=$(echo get_default_active_thread_percentage | $CTL)%" >&2

    echo "export CUDA_MPS_PIPE_DIRECTORY=$CUDA_MPS_PIPE_DIRECTORY"
    echo "export CUDA_MPS_LOG_DIRECTORY=$CUDA_MPS_LOG_DIRECTORY"
    echo "export CUDA_VISIBLE_DEVICES=$GPU"
    ;;
  stop)
    echo quit | $CTL >/dev/null 2>&1 || true
    echo "[mps] stopped" >&2
    ;;
  status)
    if daemon_up; then
      echo "[mps] running, cap=$(echo get_default_active_thread_percentage | $CTL)%, dir=$MPS_DIR"
    else
      echo "[mps] not running (dir=$MPS_DIR)"
      exit 1
    fi
    ;;
  *) usage ;;
esac
