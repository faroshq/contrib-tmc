# TMC - Transparent Multi-Cluster

WIP - This is a work in progress

## What is TMC?

TMC is a multi-cluster management platform that allows you to manage multiple Kubernetes clusters from a single control plane. TMC should be plugin of KCP
and extend its functionality to support multi-cluster workload management.

## Quick start

```
# Start TMC-KCP
go run ./cmd/tmc start

# Create TMC workspace
kubectl workspace create tmc-ws --type tmc --enter

# Create SyncTarget for remote cluster
kubectl workload sync cluster-1 --syncer-image foo:foo --output-file cluster-1.yaml

```

## Background

https://github.com/kcp-dev/kcp/issues/2954

7
