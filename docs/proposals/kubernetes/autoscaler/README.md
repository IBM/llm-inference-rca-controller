# Proposal: ResourceClaim Autoscaler for kubernetes/autoscaler

## Status
**Draft** - Not yet submitted

## Overview

Proposal to add ResourceClaim autoscaling capabilities to the kubernetes/autoscaler project as a new component alongside VPA.

## Goal

Enable automatic scaling of DRA ResourceClaims based on workload metrics, providing fine-grained resource optimization for LLM inference workloads.

## Target Repository

[kubernetes/autoscaler](https://github.com/kubernetes/autoscaler)

## Links

- [Proposal Draft](https://github.com/sunya-ch/k8s-autoscaler/blob/dra-autoscaler/vertical-pod-autoscaler/enhancements/NNNN-dra-recreate/README.md)

## Timeline

- 2026-06-22: Study VPA architecture and start implementation
- 2026-06-30: Finsih initial implementation with e2e test
- 2026-07-01: Write proposal draft
