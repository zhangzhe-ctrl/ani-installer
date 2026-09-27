## Metrics Server (B03)

- Provider: `APIService/v1beta1.metrics.k8s.io` → `Service/ani-metrics-server` in `kube-system`.
- API: `/apis/metrics.k8s.io/v1beta1/namespaces/{namespace}/pods/{pod}` through the Kubernetes API server.
- API aggregation verifies the Chart-generated serving certificate; the provider verifies kubelet serving certificates against the cluster CA and each node InternalIP SAN.
- The B03 functional checker creates and removes only its own CPU test Pod after fresh CPU and memory data are observed.
