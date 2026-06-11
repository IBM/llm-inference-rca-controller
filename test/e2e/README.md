# End-to-End (E2E) Tests

This directory contains end-to-end tests for the rca-controller project. These tests validate the complete functionality of the ResourceClaimAutoscaler and RCAControllerConfig controllers in a real Kubernetes environment.

## Overview

The e2e tests use:
- **Ginkgo/Gomega**: BDD-style testing framework
- **Kind**: Kubernetes in Docker for local testing
- **Kubernetes 1.35**: With Dynamic Resource Allocation (DRA) feature gates enabled

## Prerequisites

### Local Development
- Go 1.23 or later
- Docker
- Kind v0.25.0 or later (for Kubernetes 1.35 support)
- kubectl

### CI/CD
The GitHub Actions workflow automatically sets up the required environment.

## Running Tests

### Quick Start

Run all e2e tests with the default setup:

```bash
make test-e2e
```

This command will:
1. Create a Kind cluster with DRA feature gates enabled (if not exists)
2. Run all e2e tests
3. Clean up the cluster after tests complete

### Manual Setup

For more control over the test environment:

```bash
# 1. Create Kind cluster with custom config
kind create cluster --name rca-controller-test-e2e --config demo/kind-config.yaml --wait 5m

# 2. Verify cluster
kubectl cluster-info
kubectl get nodes

# 3. Run tests
KIND_CLUSTER=rca-controller-test-e2e go test ./test/e2e/ -v -ginkgo.v

# 4. Cleanup
kind delete cluster --name rca-controller-test-e2e
```

### Running Specific Tests

Run tests matching a pattern:

```bash
go test ./test/e2e/ -v -ginkgo.focus="Manager"
```

Run tests with custom timeout:

```bash
go test ./test/e2e/ -v -ginkgo.v -timeout 30m
```

### Environment Variables

- `KIND_CLUSTER`: Name of the Kind cluster (default: `rca-controller-test-e2e`)
- `CERT_MANAGER_INSTALL_SKIP`: Skip CertManager installation if already present (default: `false`)

## Test Structure

### Test Suite Organization

```
test/e2e/
├── e2e_suite_test.go      # Test suite setup and teardown
├── e2e_test.go            # Main test specifications
└── README.md              # This file

test/utils/
└── utils.go               # Utility functions for tests
```

### Test Scenarios

The e2e tests cover:

1. **Controller Health**
   - Controller pod deployment and readiness
   - Metrics endpoint availability
   - CRD installation verification

2. **RCAControllerConfig**
   - Configuration creation and validation
   - Prometheus endpoint connectivity
   - Custom query validation
   - Configuration updates

3. **ResourceClaimAutoscaler**
   - Basic RCA creation
   - Service and resource discovery
   - Metrics collection from Prometheus
   - Scaling decision logic
   - ResourceClaim template management
   - Status field updates

4. **Integration Scenarios**
   - Full scaling workflow (scale-up and scale-down)
   - Multiple RCAs in the same cluster
   - Configuration propagation

5. **Error Handling**
   - Missing service handling
   - Prometheus unavailability
   - Invalid metrics handling
   - Resource conflicts

## Kind Cluster Configuration

The tests use a custom Kind configuration (`demo/kind-config.yaml`) that enables:

### Feature Gates
- `DynamicResourceAllocation`: Core DRA functionality
- `DRAConsumableCapacity`: Consumable capacity tracking
- `DRAPartitionableDevices`: Device partitioning support
- `DRAPrioritizedList`: Priority-based device selection
- `DRAResourceClaimDeviceStatus`: Device status in claims
- `DRADeviceTaints`: Device tainting support
- `DRADeviceBindingConditions`: Binding condition tracking

### Cluster Topology
- 1 control-plane node
- 1 worker node
- CDI (Container Device Interface) enabled in containerd

### API Server Configuration
- `resource.k8s.io/v1beta1` API enabled
- Enhanced logging for scheduler and controller-manager

## CI/CD Integration

### GitHub Actions Workflow

The `.github/workflows/test-e2e.yml` workflow:

1. **Setup Phase**
   - Installs Go with caching
   - Installs Kind v0.25.0
   - Creates Kind cluster with custom config
   - Verifies DRA feature gates

2. **Test Phase**
   - Downloads Go dependencies
   - Runs e2e tests with 25-minute timeout
   - Collects metrics and logs

3. **Failure Handling**
   - Collects diagnostic information
   - Uploads logs as artifacts
   - Provides detailed error context

4. **Cleanup Phase**
   - Deletes Kind cluster
   - Cleans up resources

### Triggering CI Tests

Tests run automatically on:
- Push to `main` or `develop` branches
- Pull requests to `main` or `develop` branches
- Manual workflow dispatch

## Test Development

### Adding New Tests

1. Add test specifications to `e2e_test.go`:

```go
It("should handle new scenario", func() {
    By("describing the test step")
    // Test implementation
    Expect(result).To(Equal(expected))
})
```

2. Use Ginkgo's BDD style:
   - `Describe`: Group related tests
   - `Context`: Specify test conditions
   - `It`: Define individual test cases
   - `By`: Document test steps

3. Follow existing patterns:
   - Use `Eventually` for async operations
   - Set appropriate timeouts
   - Clean up resources in `AfterEach`

### Test Utilities

Common utilities in `test/utils/utils.go`:

- `Run(cmd)`: Execute commands with proper error handling
- `GetNonEmptyLines(output)`: Parse command output
- `LoadImageToKindClusterWithName(image)`: Load Docker images
- `InstallCertManager()`: Install CertManager
- `IsCertManagerCRDsInstalled()`: Check CertManager presence

### Best Practices

1. **Isolation**: Each test should be independent
2. **Cleanup**: Always clean up resources in `AfterEach`
3. **Timeouts**: Set realistic timeouts for operations
4. **Logging**: Use `GinkgoWriter` for test output
5. **Assertions**: Use descriptive assertion messages
6. **Debugging**: Collect logs on failure for troubleshooting

## Debugging Failed Tests

### Local Debugging

1. Keep the cluster running after test failure:

```bash
# Run tests without cleanup
KIND_CLUSTER=rca-controller-test-e2e go test ./test/e2e/ -v -ginkgo.v
# Don't run cleanup-test-e2e
```

2. Inspect the cluster:

```bash
kubectl get pods --all-namespaces
kubectl logs -n rca-controller-system -l control-plane=controller-manager
kubectl get events --all-namespaces --sort-by='.lastTimestamp'
kubectl describe resourceclaimautoscaler <name>
```

3. Access the cluster:

```bash
kubectl config use-context kind-rca-controller-test-e2e
```

### CI Debugging

1. Check the workflow run logs in GitHub Actions
2. Download the `e2e-test-logs` artifact for detailed diagnostics
3. Review the "Collect logs on failure" step output

### Common Issues

**Issue**: Tests timeout waiting for controller pod
- **Solution**: Check controller logs for startup errors
- **Check**: Verify image was built and loaded correctly

**Issue**: CRD not found errors
- **Solution**: Ensure `make install` completed successfully
- **Check**: Run `kubectl get crds` to verify CRD installation

**Issue**: Prometheus connection failures
- **Solution**: Verify mock Prometheus service is running
- **Check**: Test endpoint connectivity from within cluster

**Issue**: DRA feature gates not enabled
- **Solution**: Verify Kind cluster was created with custom config
- **Check**: Run `kubectl get --raw /api/resource.k8s.io/v1beta1`

## Performance Considerations

### Test Execution Time

- **Smoke tests**: ~5-10 minutes
- **Full test suite**: ~20-30 minutes
- **With cleanup**: Add 2-3 minutes

### Optimization Tips

1. **Parallel execution**: Tests in different namespaces can run in parallel
2. **Resource limits**: Set appropriate resource limits for test workloads
3. **Caching**: Use Go module caching in CI
4. **Selective testing**: Use `-ginkgo.focus` for targeted test runs

## Future Enhancements

Planned improvements for e2e tests:

1. **Chaos Testing**: Inject failures to test resilience
2. **Load Testing**: Test with high metric volumes
3. **Multi-Cluster**: Test federation scenarios
4. **Upgrade Testing**: Test version upgrades
5. **Security Testing**: Test RBAC and security policies
6. **Performance Benchmarks**: Track performance metrics over time

## References

- [Ginkgo Documentation](https://onsi.github.io/ginkgo/)
- [Gomega Matchers](https://onsi.github.io/gomega/)
- [Kind Documentation](https://kind.sigs.k8s.io/)
- [Kubernetes E2E Testing](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/e2e-tests.md)
- [Controller Runtime Testing](https://book.kubebuilder.io/reference/testing.html)
- [Dynamic Resource Allocation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)

## Support

For issues or questions:
1. Check existing GitHub issues
2. Review test logs and diagnostics
3. Consult the main project documentation
4. Open a new issue with test failure details