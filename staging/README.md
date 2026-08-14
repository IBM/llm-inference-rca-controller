# Staging

This repository serves as a staging area for developing and validating projects that may eventually be separated into independent projects and contributed upstream.

## Purpose

The projects in this repository are currently developed together to make experimentation, integration, and iteration easier.

Each project under `projects/` should be structured as independently as practical, with clear boundaries and minimal dependencies on other projects.

## Owned Subprojects

These projects live directly in this repository under `staging/projects/` and are developed and maintained here.

| Project | Path | Description |
|---------|------|-------------|
| prometheus-vpa-recommender | `staging/projects/prometheus-vpa-recommender` | VPA recommender backed by Prometheus metrics |

## External Submodules

These projects are developed in external repositories and included here as Git submodules for integration and testing.

| Project | Path | Repository | Branch |
|---------|------|------------|--------|
| dra-example-driver | `staging/projects/dra-example-driver` | [kubernetes-sigs/dra-example-driver](https://github.com/kubernetes-sigs/dra-example-driver) | *(default)* |
| k8s-autoscaler | `staging/projects/k8s-autoscaler` | [sunya-ch/k8s-autoscaler](https://github.com/sunya-ch/k8s-autoscaler) | `dra-autoscaler-local` |
| llm-d-workload-variant-autoscaler | `staging/projects/llm-d-workload-variant-autoscaler` | [sunya-ch/llm-d-workload-variant-autoscaler](https://github.com/sunya-ch/llm-d-workload-variant-autoscaler) | `implement-vertical` |

## Working with Submodules

### Initial clone

After cloning this repository, initialise and fetch all submodules:

```sh
git clone --recurse-submodules <this-repo-url>
```

Or, if you already cloned without `--recurse-submodules`:

```sh
git submodule update --init --recursive
```

### Keeping submodules in sync

After pulling changes to this repository that update submodule pointers:

```sh
git submodule update --recursive
```

To pull the latest commit from each submodule's tracked branch (updates the pinned commit):

```sh
git submodule update --remote --merge
git add staging/projects/
git commit -m "chore: update submodules to latest"
```

### Checking submodule status

```sh
git submodule status
```

### Adding a new submodule

```sh
git submodule add -b <branch> <repo-url> staging/projects/<name>
git add .gitmodules staging/projects/<name>
git commit -m "Add <name> submodule to staging/projects"
```

Then update the table in this file accordingly.
