# Proposal: In-Place ResourceClaim Resize for Kubernetes DRA

## Status

**Draft** - Not yet submitted

## Overview

Proposal to add in-place resize capability to Kubernetes Dynamic Resource Allocation (DRA), allowing ResourceClaims to be resized without pod recreation.

## Goal

Eliminate cold-start overhead during resource scaling by enabling warm-start resizing of DRA resources, particularly beneficial for large LLM models.

## Target Repository

[kubernetes/kubernetes](https://github.com/kubernetes/kubernetes) (KEP required)

## Timeline

TBD
