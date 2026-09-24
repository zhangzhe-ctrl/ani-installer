# 本轮文档校准的代码证据

来源：上传installer代码快照，只读查看；不是新的实装记录。这里只选与范围有关的行，不包含凭据。

## kubekey/pkg/ani/config.go

```text
718: 		nodeAddresses = append(nodeAddresses, n.Address)
719: 	}
720: 	return map[string]any{
721: 		"zone": "",
722: 		"download": map[string]any{
723: 			"fetch":         false,
724: 			"artifact_file": artifactPath,
725: 		},
726: 		"kubernetes": map[string]any{
727: 			"kube_version": "v1.35.8",
728: 			"cluster_name": c.Name,
729: 			"control_plane_endpoint": map[string]any{
730: 				"type": "local",
731: 			},
732: 			"custom_labels": map[string]any{
733: 				"networking.kubercloud.com/role": "master",
734: 			},
735: 		},
736: 		"etcd": map[string]any{
```

## kubekey/ani/images.tsv

```text
2: docker.io/library/haproxy:2.9.6-alpine	127.0.0.1:5000/library/haproxy:2.9.6-alpine	sha256:e563b715517db98465f12b3d8b756ef9b1e438825481351049f6f5e7796175a5	KubeKey artifact; unused because control_plane_endpoint.type=local
3: docker.io/plndr/kube-vip:v0.7.2	127.0.0.1:5000/plndr/kube-vip:v0.7.2	sha256:9d12df52ee3d95d4bc28fe19fcddb970f10705ac59566070d8dc142f593c4277	KubeKey artifact; unused because control_plane_endpoint.type=local
```

## kubekey/ani/components.lock.yaml

```text
36:   # 2026-09-21 二次供料（fix2，K-5 STS 原地重建修复）：安装清单重写自上游
39:   #（fix2 tar 为纯 docker-archive 无 index，manifest 为确定性构造，见 /tmp/kcn-tar-report.md）。
227: # kcn dev 材料最新为 2026-09-21 二次供料 fix2（K-5 STS 原地重建修复，见 base.kcn），
```

## kubekey/AGENTS.md

```text
```

## 本包采用的结论

kcn材料是否就绪以实际可读归档/摘要为准；旧AGENTS注释不能覆盖新证据，新锁注释也不能证明实机已通过。
HAProxy/kube-vip在当前local控制面路径不执行，保留版本材料维护记录，不扩成当前HA架构整改。
用户后续明确的kcn专属Envoy隔离要求覆盖旧版本文件的“以后解耦”条款。
