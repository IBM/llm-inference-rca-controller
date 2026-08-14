# Troubleshooting

## `no metrics found for variant_name: X ...`

The Prometheus query returned an empty vector.

1. **Confirm the metric exists.** Port-forward to Prometheus and run the exact
   query shown in the recommender logs (enable `--v=5` to see it):

   ```bash
   kubectl port-forward -n <prometheus-namespace> svc/<prometheus-svc> 9090:9090

   curl -sg 'http://localhost:9090/api/v1/query' \
     --data-urlencode 'query=wva_desired_capacity_per_device{variant_name="sample-deployment",target_container="dev-model-decode",accelerator_type="gpu.example.com",capacity="compute"}' \
     | jq .
   ```

2. **Check label values match exactly:**
   - `variant_name` = `VPA.metadata.name`
   - `target_container` = container name in the Deployment that references the claim
   - `accelerator_type` = `resourceClaimPolicy.deviceClassName`
   - `capacity` = entry in `resourceClaimPolicy.controlledCapacities`

3. **Check the metric name.** The recommender queries `--capacity-metric-name`
   (default `desired_capacity`). For WVA, set
   `--capacity-metric-name=wva_desired_capacity_per_device`.

---

## `failed to query Prometheus: ... x509: certificate signed by unknown authority`

Prometheus is using a self-signed TLS certificate. Either:

- Set `--prometheus-insecure-skip-verify=true`, or
- Use `PROMETHEUS_INSECURE_SKIP_VERIFY=true` in `make deploy`.

---

## `no containers in deployment X/Y reference a claim template managed by this VPA`

The recommender could not find a container in the target Deployment that
references one of the claim templates listed in the VPA's `resourceClaimPolicies`.

- The VPA's `claimTemplateName` must match a name under
  `Deployment.spec.template.spec.resourceClaims`.
- The container's `resources.claims` must reference that claim name.

---

## `failed to list VPAs: ... Forbidden`

The recommender's ServiceAccount lacks RBAC permissions. Re-apply the RBAC manifest:

```bash
kubectl apply -f deploy/rbac.yaml
```

---

## VPA status is not updating

1. Check the recommender is running and healthy:

   ```bash
   make status
   make logs
   ```

2. Confirm the VPA has this recommender listed:

   ```bash
   kubectl get vpa <name> -o jsonpath='{.spec.recommenders}'
   # should print: [{"name":"prometheus"}]
   ```

3. Increase verbosity to see every query cycle:

   ```bash
   kubectl edit deployment -n kube-system prometheus-vpa-recommender
   # change --v=4 to --v=5
   ```
