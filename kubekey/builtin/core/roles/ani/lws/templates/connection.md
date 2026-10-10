# LeaderWorkerSet

Official LWS {{ (index .ani.components "lws").version }} in `lws-system`, Helm release `ani-lws`.
API: `leaderworkerset.x-k8s.io/v1`; deployment: `lws-controller-manager`.
Controller: `{{ (index .ani.components "lws").image_ref }}`.
Webhook certificate is owned by LWS's internal certificate manager; cert-manager is not required.
The official chart includes controller-required DisaggregatedSet APIs. No DisaggregatedSet workload is selected.
CPU HTTP probes verify scheduling and group lifecycle at the external inference engine boundary.
Real LLM engines, model loading and ANI business acceptance require separate verification.
