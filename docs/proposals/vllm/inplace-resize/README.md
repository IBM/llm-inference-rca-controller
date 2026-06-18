# Proposal: In-Place GPU Memory Resize for vLLM

## Status

**Draft** - Not yet submitted

## Overview

Proposal to add dynamic GPU resource allocation and model reloading capabilities to vLLM, enabling warm-start resource resizing without service interruption.

## Goal

Allow vLLM to dynamically adjust GPU resource allocation and reload model components in response to ResourceClaim changes, eliminating cold-start overhead during scaling.

## Target Repository

[vllm-project/vllm](https://github.com/vllm-project/vllm)

## Timeline

TBD
