"""Bounded ANI tenant, TLS entry and single-process runtime resources.

Materialized by the reviewed first-install role. No cluster access in this
module; generated objects must pass the actual API's strict dry-run first.
"""
import json

RELEASE = "26.03-kfp2.16-trainer2.1-v1"
EXECUTION_IMAGE = "ani.local/kubeflow-execution:26.03-v1"


def obj(kind, name, namespace=None, api="v1", **body):
    metadata = {"name": name, "labels": {"ani.io/kubeflow-release": RELEASE}}
    if namespace:
        metadata["namespace"] = namespace
    return dict(apiVersion=api, kind=kind, metadata=metadata, **body)


def rb(name, namespace, role, account, account_namespace=None, cluster_role=False):
    return obj("RoleBinding", name, namespace, api="rbac.authorization.k8s.io/v1",
               roleRef={"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole" if cluster_role else "Role", "name": role},
               subjects=[{"kind": "ServiceAccount", "name": account, "namespace": account_namespace or namespace}])


def role(name, namespace, rules):
    return obj("Role", name, namespace, api="rbac.authorization.k8s.io/v1", rules=rules)


def rule(group, resources, verbs, names=None):
    value = {"apiGroups": [group], "resources": resources, "verbs": verbs}
    if names is not None:
        value["resourceNames"] = names
    return value


def network(name, namespace, selector, ingress=None, egress=None):
    spec = {"podSelector": {"matchLabels": selector}, "policyTypes": []}
    if ingress is not None:
        spec.update(ingress=ingress); spec["policyTypes"].append("Ingress")
    if egress is not None:
        spec.update(egress=egress); spec["policyTypes"].append("Egress")
    return obj("NetworkPolicy", name, namespace, api="networking.k8s.io/v1", spec=spec)


def peers(namespace, selector=None):
    peer = {"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": namespace}}}
    if selector is not None:
        peer["podSelector"] = {"matchLabels": selector}
    return peer


def ports(*values):
    return [{"protocol": "TCP", "port": value} for value in values]


NGINX = """pid /tmp/nginx.pid;
error_log /dev/stderr warn;
events { worker_connections 1024; }
http {
  access_log off;
  client_body_temp_path /tmp/client;
  proxy_temp_path /tmp/proxy;
  grpc_socket_keepalive on;
  underscores_in_headers on;
  ignore_invalid_headers on;
  map "$http_kubeflow_userid$http_x_goog_authenticated_user_email$http_x_auth_request_user$http_x_auth_request_email$http_remote_user$http_x_forwarded_user" $forged_identity {
    "" 0; default 1;
  }
  server {
    listen 8443 ssl;
    ssl_certificate /etc/kfp-tls/tls.crt;
    ssl_certificate_key /etc/kfp-tls/tls.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    client_max_body_size 20m;
    if ($forged_identity) { return 400; }
    location = /healthz { return 200 "tls-entry-alive\\n"; }
    location / {
      proxy_pass http://ml-pipeline.kubeflow.svc.cluster.local:8888;
      proxy_http_version 1.1;
      proxy_set_header Authorization $http_authorization;
      proxy_set_header kubeflow-userid "";
      proxy_set_header x-goog-authenticated-user-email "";
      proxy_set_header X-Forwarded-User "";
      proxy_set_header X-Auth-Request-User "";
      proxy_set_header X-Auth-Request-Email "";
      proxy_read_timeout 120s;
    }
  }
  server {
    listen 8444 ssl;
    http2 on;
    ssl_certificate /etc/kfp-tls/tls.crt;
    ssl_certificate_key /etc/kfp-tls/tls.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    if ($forged_identity) { return 400; }
    location / {
      grpc_pass grpc://ml-pipeline.kubeflow.svc.cluster.local:8887;
      grpc_set_header Authorization $http_authorization;
      grpc_set_header kubeflow-userid "";
      grpc_set_header x-goog-authenticated-user-email "";
      grpc_set_header X-Forwarded-User "";
      grpc_set_header X-Auth-Request-User "";
      grpc_set_header X-Auth-Request-Email "";
      grpc_read_timeout 120s;
    }
  }
}
"""


def entry(site, images):
    labels = {"ani.io/kfp-role": "entry"}
    security = {"runAsNonRoot": True, "runAsUser": 1000, "runAsGroup": 1000,
                "allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True,
                "capabilities": {"drop": ["ALL"]}, "seccompProfile": {"type": "RuntimeDefault"}}
    return [obj("ServiceAccount", "ani-kfp-entry", "kubeflow", automountServiceAccountToken=False),
        obj("Certificate", "ani-kfp-entry", "kubeflow", api="cert-manager.io/v1", spec={
            "secretName": "ani-kfp-entry-tls", "issuerRef": {"name": "ani-ca", "kind": "ClusterIssuer"},
            "dnsNames": ["ani-kfp-entry.kubeflow.svc", "ani-kfp-entry.kubeflow.svc.cluster.local"],
            "ipAddresses": [site["entry_address"]], "usages": ["server auth"]}),
        obj("ConfigMap", "ani-kfp-entry", "kubeflow", data={"nginx.conf": NGINX}),
        obj("Service", "ani-kfp-entry", "kubeflow", spec={"type": "NodePort", "selector": labels,
            "ports": [{"name": "https", "port": 8443, "targetPort": 8443, "nodePort": site["http_port"]},
                      {"name": "grpcs", "port": 8444, "targetPort": 8444, "nodePort": site["grpc_port"]}]}),
        obj("Deployment", "ani-kfp-entry", "kubeflow", api="apps/v1", spec={"replicas": 1,
            "selector": {"matchLabels": labels}, "template": {"metadata": {"labels": labels}, "spec": {
                "automountServiceAccountToken": False, "serviceAccountName": "ani-kfp-entry",
                "securityContext": {"fsGroup": 1000}, "containers": [{"name": "nginx",
                    "image": images["docker.io/library/nginx:1.30.5"], "command": ["nginx"], "args": ["-g", "daemon off;"],
                    "securityContext": security, "resources": {"requests": {"cpu": "100m", "memory": "64Mi"}, "limits": {"cpu": "1", "memory": "256Mi"}},
                    "ports": [{"containerPort": 8443}, {"containerPort": 8444}],
                    "readinessProbe": {"tcpSocket": {"port": 8443}, "periodSeconds": 5},
                    "volumeMounts": [{"name": "config", "mountPath": "/etc/nginx/nginx.conf", "subPath": "nginx.conf", "readOnly": True},
                                     {"name": "tls", "mountPath": "/etc/kfp-tls", "readOnly": True}, {"name": "tmp", "mountPath": "/tmp"}]}],
                "volumes": [{"name": "config", "configMap": {"name": "ani-kfp-entry"}},
                            {"name": "tls", "secret": {"secretName": "ani-kfp-entry-tls", "defaultMode": 288}},
                            {"name": "tmp", "emptyDir": {}}]}}})]


def tenant(site, namespace, mysql_api_rules):
    # Environment identities: CPU02 must bind/reverify the actual service SA.
    ns = obj("Namespace", namespace)
    ns["metadata"]["labels"].update({"ani.io/kubeflow-tenant": "true", "pod-security.kubernetes.io/enforce": "restricted"})
    output = [ns]
    for name in ("api-client", "pipeline-runner", "trainer-workload"):
        output.append(obj("ServiceAccount", name, namespace, automountServiceAccountToken=name == "pipeline-runner"))
    api_rules = [rule("pipelines.kubeflow.org", ["pipelines", "pipelines/versions", "experiments", "runs", "jobs", "recurringruns", "artifacts", "tasks"], ["get", "list", "create", "update", "delete"])]
    output += [role("ani-kfp-api-client", namespace, api_rules), rb("ani-kfp-api-client", namespace, "ani-kfp-api-client", "api-client"),
               rb("ani-kfp-runner-api", namespace, "ani-kfp-api-client", "pipeline-runner")]
    runner_rules = [rule("", ["configmaps"], ["get", "list", "watch"]),
        rule("", ["secrets"], ["get"], ["mlpipeline-minio-artifact", "ani-kfp-ca"]),
        rule("", ["persistentvolumeclaims"], ["create", "get", "list", "watch"]),
        rule("argoproj.io", ["workflows"], ["get", "list", "watch", "patch", "update"]),
        rule("argoproj.io", ["workflowtaskresults"], ["create", "patch"]),
        rule("trainer.kubeflow.org", ["trainjobs"], ["create", "get", "list", "watch", "patch", "delete"]),
        rule("jobset.x-k8s.io", ["jobsets"], ["get", "list", "watch"]),
        rule("", ["pods", "pods/log"], ["get", "list", "watch"])]
    output += [role("pipeline-runner", namespace, runner_rules), rb("pipeline-runner", namespace, "pipeline-runner", "pipeline-runner"),
               role("ml-pipeline", namespace, mysql_api_rules), rb("ml-pipeline", namespace, "ml-pipeline", "ml-pipeline", "kubeflow")]
    provider = {"s3": {"default": {"endpoint": "ani-rustfs-svc.ani-platform.svc.cluster.local:9000", "region": "us-east-1",
        "disableSSL": False, "forcePathStyle": True, "maxRetries": 2,
        "credentials": {"fromEnv": False, "secretRef": {"secretName": "mlpipeline-minio-artifact", "accessKeyKey": "accesskey", "secretKeyKey": "secretkey"}}}}}
    output += [obj("ConfigMap", "kfp-launcher", namespace, data={"defaultPipelineRoot": "s3://ani-kfp-" + namespace + "/artifacts", "clusterDomain": "cluster.local", "providers": json.dumps(provider)}),
        obj("ConfigMap", "metadata-grpc-configmap", namespace, data={"METADATA_GRPC_SERVICE_HOST": "metadata-grpc-service.kubeflow.svc.cluster.local", "METADATA_GRPC_SERVICE_PORT": "8080"})]
    repository = {"archiveLogs": True, "s3": {"endpoint": "ani-rustfs-svc.ani-platform.svc.cluster.local:9000",
        "bucket": "ani-kfp-" + namespace, "region": "us-east-1", "insecure": False,
        "keyFormat": "artifacts/argo/{{workflow.uid}}/{{pod.name}}",
        "caSecret": {"name": "ani-kfp-ca", "key": "ca.crt"},
        "accessKeySecret": {"name": "mlpipeline-minio-artifact", "key": "accesskey"},
        "secretKeySecret": {"name": "mlpipeline-minio-artifact", "key": "secretkey"}}}
    output.append(obj("ConfigMap", "artifact-repositories", namespace, data={"default-v1": json.dumps(repository)}))
    output[-1]["metadata"]["annotations"] = {"workflows.argoproj.io/default-artifact-repository": "default-v1"}
    return output


def admission(site, runtime_name, image):
    selector = {"matchLabels": {"ani.io/kubeflow-tenant": "true"}}
    policies = {
        "ani-kfp-workspace": ("", "v1", "persistentvolumeclaims", [
            ("object.metadata.name.startsWith('ani-kfp-workspace-')", "Use a managed per-execution workspace claim"),
            ("has(object.spec.storageClassName) && object.spec.storageClassName == params.data.storageClass", "Workspace StorageClass is administrator-controlled"),
            ("quantity(string(object.spec.resources.requests['storage'])).compareTo(quantity('0')) > 0 && quantity(string(object.spec.resources.requests['storage'])).compareTo(quantity(params.data.maxSize)) <= 0", "Workspace storage exceeds the administrator bound"),
            ("object.spec.accessModes == ['ReadWriteMany']", "Workspace requires declared RWX storage"),
            ("!has(object.metadata.ownerReferences) || size(object.metadata.ownerReferences) == 0", "Workspace must survive Workflow garbage collection"),
        ]),
        "ani-kfp-trainjob": ("trainer.kubeflow.org", "v1alpha1", "trainjobs", [
            ("object.spec.runtimeRef.name == params.data.runtimeName && (!has(object.spec.runtimeRef.kind) || object.spec.runtimeRef.kind == 'ClusterTrainingRuntime') && (!has(object.spec.runtimeRef.apiGroup) || object.spec.runtimeRef.apiGroup == 'trainer.kubeflow.org')", "Use the fixed versioned runtime"),
            ("!has(object.spec.managedBy) || object.spec.managedBy == 'trainer.kubeflow.org/trainjob-controller'", "Use the installed TrainJob controller"),
            ("!has(object.spec.trainer) || !has(object.spec.trainer.numNodes) || object.spec.trainer.numNodes == 1", "This runtime supports one node"),
            ("!has(object.spec.trainer) || !has(object.spec.trainer.numProcPerNode) || object.spec.trainer.numProcPerNode == 1", "This runtime supports one process"),
            ("!has(object.spec.initializer)", "This runtime does not provision initializer credentials or jobs"),
            ("!has(object.spec.trainer) || !has(object.spec.trainer.resourcesPerNode) || ((!has(object.spec.trainer.resourcesPerNode.requests) || object.spec.trainer.resourcesPerNode.requests.all(k, k in ['cpu', 'memory'])) && (!has(object.spec.trainer.resourcesPerNode.limits) || object.spec.trainer.resourcesPerNode.limits.all(k, k in ['cpu', 'memory'])))", "Only CPU and memory resource overrides are supported"),
            ("!has(object.spec.trainer) || !has(object.spec.trainer.resourcesPerNode) || !has(object.spec.trainer.resourcesPerNode.requests) || object.spec.trainer.resourcesPerNode.requests.all(k, quantity(string(object.spec.trainer.resourcesPerNode.requests[k])).compareTo(quantity('0')) > 0 && quantity(string(object.spec.trainer.resourcesPerNode.requests[k])).compareTo(quantity(k == 'cpu' ? '2' : '4Gi')) <= 0)", "Training resource requests exceed the administrator bound"),
            ("!has(object.spec.trainer) || !has(object.spec.trainer.resourcesPerNode) || !has(object.spec.trainer.resourcesPerNode.limits) || object.spec.trainer.resourcesPerNode.limits.all(k, quantity(string(object.spec.trainer.resourcesPerNode.limits[k])).compareTo(quantity('0')) > 0 && quantity(string(object.spec.trainer.resourcesPerNode.limits[k])).compareTo(quantity(k == 'cpu' ? '2' : '4Gi')) <= 0)", "Training resource limits exceed the administrator bound"),
            ("!has(object.spec.trainer) || !has(object.spec.trainer.image) || object.spec.trainer.image == params.data.image", "Runtime image overrides must preserve the approved digest"),
            ("!has(object.spec.trainer) || !has(object.spec.trainer.env) || object.spec.trainer.env.all(e, !has(e.valueFrom))", "Training containers cannot acquire Secret or token references"),
            ("!has(object.spec.labels) || object.spec.labels.all(k, k in ['ani.io/execution-id'])", "Training labels cannot impersonate pipeline control pods"),
            ("!has(object.spec.podTemplateOverrides) || object.spec.podTemplateOverrides.all(p, !has(p.metadata) || (!has(p.metadata.labels) || p.metadata.labels.all(k, k in ['ani.io/execution-id'])))", "Pod overrides cannot acquire control network labels"),
            ("!has(object.spec.podTemplateOverrides) || object.spec.podTemplateOverrides.all(p, !has(p.spec) || !has(p.spec.serviceAccountName) || p.spec.serviceAccountName == 'trainer-workload')", "Training service account is fixed and unprivileged"),
            ("!has(object.spec.podTemplateOverrides) || object.spec.podTemplateOverrides.all(p, !has(p.spec) || ((!has(p.spec.initContainers) || size(p.spec.initContainers) == 0) && (!has(p.spec.imagePullSecrets) || size(p.spec.imagePullSecrets) == 0)))", "Training overrides cannot acquire initializer or image-pull Secrets"),
            ("!has(object.spec.podTemplateOverrides) || object.spec.podTemplateOverrides.all(p, !has(p.spec) || !has(p.spec.volumes) || p.spec.volumes.all(v, has(v.persistentVolumeClaim) && v.persistentVolumeClaim.claimName.startsWith('ani-kfp-workspace-')))", "Only explicit execution PVC volumes are allowed"),
            ("!has(object.spec.podTemplateOverrides) || object.spec.podTemplateOverrides.all(p, !has(p.spec) || !has(p.spec.containers) || p.spec.containers.all(c, !has(c.env) || c.env.all(e, !has(e.valueFrom))))", "Container overrides cannot acquire Secret or token references"),
        ]),
    }
    output = []
    for name, (group, version, resource, expressions) in policies.items():
        output.append(obj("ValidatingAdmissionPolicy", name, api="admissionregistration.k8s.io/v1", spec={
            "failurePolicy": "Fail", "paramKind": {"apiVersion": "v1", "kind": "ConfigMap"},
            "matchConstraints": {"resourceRules": [{"apiGroups": [group], "apiVersions": [version], "operations": ["CREATE", "UPDATE"], "resources": [resource]}]},
            "validations": [{"expression": expression, "message": message} for expression, message in expressions]}))
        output.append(obj("ValidatingAdmissionPolicyBinding", name, api="admissionregistration.k8s.io/v1", spec={
            "policyName": name, "paramRef": {"name": "ani-workspace-policy", "parameterNotFoundAction": "Deny"},
            "validationActions": ["Deny"], "matchResources": {"namespaceSelector": selector}}))
    for namespace in site["tenants"]:
        output.append(obj("ConfigMap", "ani-workspace-policy", namespace, data={"storageClass": site["workspace_class"],
            "maxSize": site["workspace_max_size"], "runtimeName": runtime_name, "image": image}))
        output.append(obj("ResourceQuota", "ani-kfp-execution-budget", namespace, spec={"hard": {
            "count/persistentvolumeclaims": str(site["workspace_max_claims"]), "requests.storage": site["workspace_quota_size"],
            "requests.cpu": "6", "requests.memory": "12Gi", "limits.cpu": "8", "limits.memory": "16Gi", "count/pods": "30"}}))
        output.append(obj("LimitRange", "ani-kfp-container-budget", namespace, spec={"limits": [{"type": "Container",
            "defaultRequest": {"cpu": "100m", "memory": "128Mi"}, "default": {"cpu": "500m", "memory": "512Mi"},
            "max": {"cpu": "2", "memory": "4Gi"}}]}))
    return output


def isolation(site):
    output = [network("ani-kfp-default-ingress", "kubeflow", {}, ingress=[])]
    pipeline_peer = {"namespaceSelector": {"matchLabels": {"ani.io/kubeflow-tenant": "true"}},
                     "podSelector": {"matchLabels": {"pipelines.kubeflow.org/v2_component": "true"}}}
    control_peer = peers("kubeflow")
    output += [network("ani-kfp-api", "kubeflow", {"ani.io/kfp-role": "ml-pipeline"}, ingress=[
        {"from": [control_peer, pipeline_peer], "ports": ports(8888, 8887)}]),
        network("ani-kfp-mlmd", "kubeflow", {"ani.io/kfp-role": "metadata-grpc-deployment"}, ingress=[
            {"from": [control_peer, pipeline_peer], "ports": ports(8080)}]),
        network("ani-kfp-mysql", "kubeflow", {"ani.io/kfp-role": "mysql"}, ingress=[
            {"from": [peers("kubeflow", {"ani.io/kfp-role": name}) for name in ("ml-pipeline", "metadata-grpc-deployment")], "ports": ports(3306)}]),
        network("ani-kfp-entry", "kubeflow", {"ani.io/kfp-role": "entry"}, ingress=[{"ports": ports(8443, 8444)}]),
        network("ani-kfp-metadata-envoy", "kubeflow", {"ani.io/kfp-role": "metadata-envoy-deployment"}, ingress=[{"from": [control_peer], "ports": ports(9090)}])]
    dns = {"to": [peers("kube-system", {"k8s-app": "kube-dns"})],
           "ports": [{"protocol": "UDP", "port": 53}, {"protocol": "TCP", "port": 53}]}
    for namespace in site["tenants"]:
        output.append(network("ani-kfp-default", namespace, {}, ingress=[], egress=[dns]))
        api_destinations = [{"ipBlock": {"cidr": value + "/32"}} for value in site["node_addresses"]]
        api_destinations.append({"ipBlock": {"cidr": site["kubernetes_service_ip"] + "/32"}})
        output.append(network("ani-kfp-pipeline", namespace, {"pipelines.kubeflow.org/v2_component": "true"}, egress=[
            {"to": [control_peer], "ports": ports(8888, 8887, 8080)},
            {"to": [peers("ani-platform")], "ports": ports(9000)},
            {"to": api_destinations, "ports": ports(443, 6443)}]))
    return output


def runtime(images):
    image = images[EXECUTION_IMAGE]
    name = "ani-single-process-v1-" + image.rsplit("sha256:", 1)[1][:8]
    pod = {"automountServiceAccountToken": False, "serviceAccountName": "trainer-workload",
        "securityContext": {"runAsNonRoot": True, "runAsUser": 1000, "runAsGroup": 1000, "fsGroup": 1000, "seccompProfile": {"type": "RuntimeDefault"}},
        "containers": [{"name": "node", "image": image, "command": ["python", "-c", "print('ANI single-process runtime')"],
            "resources": {"requests": {"cpu": "1", "memory": "512Mi"}, "limits": {"cpu": "1", "memory": "2Gi"}},
            "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}}}], "restartPolicy": "Never"}
    value = obj("ClusterTrainingRuntime", name, api="trainer.kubeflow.org/v1alpha1", spec={"mlPolicy": {"numNodes": 1},
        "template": {"spec": {"failurePolicy": {"maxRestarts": 0}, "replicatedJobs": [{"name": "node", "replicas": 1,
            "template": {"metadata": {"labels": {"trainer.kubeflow.org/trainjob-ancestor-step": "trainer"}},
                         "spec": {"parallelism": 1, "completions": 1, "backoffLimit": 0, "template": {"spec": pod}}}}]}}})
    return value
