# Ceph 使用与手工验证（172.16.101.20～22）

日期：2026-09-18。以下资源名来自本次真实集群的只读检查。本文中的创建 PVC、写入数据、重建测试 Pod、创建 S3 用户等步骤，尚未执行，由用户手工运行。

## 1. 已检查到的现状

| 项目 | 实际结果 |
| --- | --- |
| 节点 | node1/.20、node2/.21、node3/.22，全部 Ready |
| 版本 | Kubernetes 1.35.8；containerd 2.3.4；Rook 1.20.7；Ceph 20.2.4；CSI 3.17.1 |
| CephCluster | rook-ceph/rook-ceph，Ready，但 HEALTH_WARN |
| MON / OSD | 三 MON 有 quorum；三 OSD 全部 up/in |
| PG / 容量 | 329 PG active+clean；约 150 GiB 原始容量，三副本池当前 MAX AVAIL 约 47 GiB |
| 块存储 | StorageClass `ani-block`，RBD，pool `ani-block-pool`，ext4 |
| 文件存储 | StorageClass `ani-cephfs`，CephFS `ani-fs`，支持共享卷 |
| 对象存储 | CephObjectStore `ani-store`，一个 RGW 实例 |
| RGW 入口 | 集群内 `http://rook-ceph-rgw-ani-store.rook-ceph.svc:80`；当前 ClusterIP `10.96.69.104` |
| PVC / 对象用户 | 检查时没有 PVC，也没有 CephObjectStoreUser |
| 网络 | MON/OSD 使用 `10.16.x.x` Pod 网络；没有配置专用 ens36 存储网络 |

三类告警必须分别看：

- AES CSI 密钥相关的三个 AUTH_INSECURE 告警：与当前显式采用 AES 的配置对应，不能标为 HEALTH_OK。
- `MON_CLOCK_SKEW`：mon.b/mon.c 与参考时钟偏差约 1.32/1.28 秒，超过其 0.05 秒阈值。
- `TOO_MANY_PGS`：每 OSD 329 PG，超过当前 250 阈值。

可以进行小量功能手测，但“读写通过”和“全部健康告警处理完成”是不同结论。不要为了验收直接调高告警阈值。本文不调整时钟、不改池/PG、不改网络、不重装集群。

## 2. 登录和只读检查

先登录 fedora，再登录 installer 节点。以下后续命令都在 `.20` 的同一个 root Bash 会话执行；密码使用已有现场凭据。

```bash
ssh fedora
ssh ubuntu@172.16.101.20
sudo -i
export KUBECONFIG=/etc/kubernetes/admin.conf
set -euo pipefail
```

目前 `/home/ubuntu/.kube/config` 不能直接用于本次访问，因此明确使用 admin.conf；无需修改 kubeconfig 文件。

```bash
kubectl get nodes -o wide
kubectl get sc ani-block ani-cephfs
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph -s
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph health detail
```

后续命令会创建独立测试资源。按顺序执行，任何一步报错就停止并保留现场；不要跳过错误继续打印成功，不重启/修复 Ceph 来掩盖失败。

## 3. 创建两类 PVC 和测试 Pod

`ani-block` 用于普通单实例应用的数据盘，下面声明 RWO。`ani-cephfs` 用于多个 Pod 共享文件，下面声明 RWX。StorageClass 创建卷，应用通过 PVC 使用，不需要手工进入 Ceph 创建 RBD image。

先生成独立 namespace，记录终端输出，后面都沿用同一个 `$NS`：

```bash
NS="ceph-manual-$(date +%Y%m%d%H%M%S)"
printf '本次测试 namespace: %s\n' "$NS"
kubectl create namespace "$NS"

kubectl -n "$NS" apply -f - <<'YAML'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: block-test
spec:
  storageClassName: ani-block
  accessModes: [ReadWriteOnce]
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: fs-test
spec:
  storageClassName: ani-cephfs
  accessModes: [ReadWriteMany]
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: writer-a
spec:
  nodeSelector:
    kubernetes.io/hostname: node1
  containers:
    - name: client
      image: 172.16.101.20:5000/library/busybox:1.37.0
      imagePullPolicy: IfNotPresent
      command: [sh, -c, 'exec sleep 86400']
      volumeMounts:
        - {name: block, mountPath: /block}
        - {name: shared, mountPath: /shared}
  volumes:
    - name: block
      persistentVolumeClaim: {claimName: block-test}
    - name: shared
      persistentVolumeClaim: {claimName: fs-test}
---
apiVersion: v1
kind: Pod
metadata:
  name: writer-b
spec:
  nodeSelector:
    kubernetes.io/hostname: node2
  containers:
    - name: client
      image: 172.16.101.20:5000/library/busybox:1.37.0
      imagePullPolicy: IfNotPresent
      command: [sh, -c, 'exec sleep 86400']
      volumeMounts:
        - {name: shared, mountPath: /shared}
  volumes:
    - name: shared
      persistentVolumeClaim: {claimName: fs-test}
YAML

kubectl -n "$NS" wait --for=jsonpath='{.status.phase}'=Bound pvc/block-test pvc/fs-test --timeout=180s
kubectl -n "$NS" wait --for=condition=Ready pod/writer-a pod/writer-b --timeout=300s
kubectl -n "$NS" get pvc,pods -o wide
```

此时只能确认卷创建、挂载和 Pod 启动，下一步才验证数据。

## 4. RBD 读写和 CephFS 跨节点共享

```bash
# node1 写入 RBD，再读回核对本次测试标识。
kubectl -n "$NS" exec writer-a -- sh -ec \
  'printf "RBD:%s\n" "$1" > /block/probe.txt; sync; test "$(cat /block/probe.txt)" = "RBD:$1"; echo RBD_WRITE_READ_OK' sh "$NS"

# node1 写共享文件。
kubectl -n "$NS" exec writer-a -- sh -ec \
  'printf "A:%s\n" "$1" > /shared/from-a.txt; sync' sh "$NS"

# node2 读 node1 的文件，并反向写入另一个文件。
kubectl -n "$NS" exec writer-b -- sh -ec \
  'test "$(cat /shared/from-a.txt)" = "A:$1"; printf "B:%s\n" "$1" > /shared/from-b.txt; sync; echo CEPHFS_A_TO_B_OK' sh "$NS"

# node1 读回 node2 写入的文件。
kubectl -n "$NS" exec writer-a -- sh -ec \
  'test "$(cat /shared/from-b.txt)" = "B:$1"; echo CEPHFS_B_TO_A_OK' sh "$NS"
```

四条命令全部成功，才能记录这两类存储的基本读写通过。

## 5. RBD 正常跨节点重新挂载

只删除本次测试的 writer-a，**不删除 PVC**。正常释放后，在 node3 创建另一个 Pod，读取原来的 RBD 数据。这是正常迁移/持久化测试，不是节点故障或 HA 测试。

```bash
kubectl -n "$NS" delete pod writer-a --wait=true --timeout=180s

kubectl -n "$NS" apply -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: reader-c
spec:
  nodeSelector:
    kubernetes.io/hostname: node3
  containers:
    - name: client
      image: 172.16.101.20:5000/library/busybox:1.37.0
      imagePullPolicy: IfNotPresent
      command: [sh, -c, 'exec sleep 86400']
      volumeMounts:
        - {name: block, mountPath: /block}
  volumes:
    - name: block
      persistentVolumeClaim: {claimName: block-test}
YAML

kubectl -n "$NS" wait --for=condition=Ready pod/reader-c --timeout=300s
kubectl -n "$NS" get pod reader-c -o wide
kubectl -n "$NS" exec reader-c -- sh -ec \
  'test "$(cat /block/probe.txt)" = "RBD:$1"; echo RBD_NODE1_TO_NODE3_PERSISTENCE_OK' sh "$NS"
```

出现 Multi-Attach、FailedMount 或超时就停止，不强制 detach、不删 VolumeAttachment、不清理 CNI/OVN。记录具体事件后判断归属。

## 6. S3 对象存储：单独创建测试用户并实际读写

S3 不使用上面的 PVC，也不是把 CephFS 当对象存储。客户端需要 RGW endpoint、access key、secret key 和 bucket。

当前没有对象用户；下列步骤会在 rook-ceph 创建一个本次专用用户。它只用于本次手测，不复用管理员身份。Rook 为该用户生成 Secret，命令仅读入变量，不打印密钥。

```bash
S3_USER="manual-${NS}"
kubectl -n rook-ceph apply -f - <<YAML
apiVersion: ceph.rook.io/v1
kind: CephObjectStoreUser
metadata:
  name: ${S3_USER}
spec:
  store: ani-store
  displayName: ${S3_USER}
YAML

kubectl -n rook-ceph wait --for=jsonpath='{.status.phase}'=Ready \
  "cephobjectstoreuser/${S3_USER}" --timeout=180s
S3_SECRET="rook-ceph-object-user-ani-store-${S3_USER}"
S3_ACCESS_KEY=$(kubectl -n rook-ceph get secret "$S3_SECRET" -o jsonpath='{.data.AccessKey}' | base64 -d)
S3_SECRET_KEY=$(kubectl -n rook-ceph get secret "$S3_SECRET" -o jsonpath='{.data.SecretKey}' | base64 -d)
test -n "$S3_ACCESS_KEY"
test -n "$S3_SECRET_KEY"

RGW_IP=$(kubectl -n rook-ceph get svc rook-ceph-rgw-ani-store -o jsonpath='{.spec.clusterIP}')
S3_ENDPOINT="http://${RGW_IP}:80"
BUCKET="$NS"
S3_TMP=$(mktemp -d /tmp/ceph-s3-manual.XXXXXX)
printf 'S3:%s\n' "$NS" > "$S3_TMP/source.txt"
EMPTY_SHA=$(printf '' | sha256sum | awk '{print $1}')
BODY_SHA=$(sha256sum "$S3_TMP/source.txt" | awk '{print $1}')

# .20 的 curl 8.5.0 已确认支持 AWS SigV4；无需安装 awscli/pip。
# 凭据通过标准输入提供给 curl，不写进命令参数或输出。
s3() {
  printf 'user = "%s:%s"\n' "$S3_ACCESS_KEY" "$S3_SECRET_KEY" |
    curl --config - --aws-sigv4 'aws:amz:us-east-1:s3' \
      --connect-timeout 5 --max-time 30 --fail-with-body --silent --show-error "$@"
}

# 建桶、上传、下载并比较实际内容。
s3 -X PUT -H "x-amz-content-sha256: $EMPTY_SHA" "$S3_ENDPOINT/$BUCKET"
s3 -X PUT -H "x-amz-content-sha256: $BODY_SHA" \
  --data-binary "@$S3_TMP/source.txt" "$S3_ENDPOINT/$BUCKET/probe.txt"
s3 -H "x-amz-content-sha256: $EMPTY_SHA" \
  "$S3_ENDPOINT/$BUCKET/probe.txt" -o "$S3_TMP/download.txt"
cmp "$S3_TMP/source.txt" "$S3_TMP/download.txt"
printf 'S3_PUT_GET_COMPARE_OK\n'

# 只删除刚才这个测试对象，保留测试桶和用户。
s3 -X DELETE -H "x-amz-content-sha256: $EMPTY_SHA" "$S3_ENDPOINT/$BUCKET/probe.txt"
if DELETE_CODE=$(s3 -H "x-amz-content-sha256: $EMPTY_SHA" \
  -o "$S3_TMP/after-delete.xml" -w '%{http_code}' \
  "$S3_ENDPOINT/$BUCKET/probe.txt" 2>"$S3_TMP/after-delete.stderr"); then
  printf '失败：删除后仍能读取对象\n' >&2
  exit 1
fi
test "$DELETE_CODE" = 404
grep -q '<Code>NoSuchKey</Code>' "$S3_TMP/after-delete.xml"
printf 'S3_DELETE_CONFIRMED_OK\n'
unset S3_ACCESS_KEY S3_SECRET_KEY
```

这些 S3 操作尚未实际执行；如认证/签名请求失败，保留 HTTP 错误码与错误体，勿贴出密钥，不自动更改 RGW 配置。简单 PUT/GET/DELETE 通过不等于完整 AWS S3 兼容、Multipart 或应用级兼容已验证。

## 7. 如何记录结果与取证

分别记录 PVC 创建、RBD 读写、RBD 跨节点重新挂载、CephFS 双向共享、S3 读写/删除为 pass/fail/not_verified。出现失败后运行下面的只读取证，不执行修复或集群重装：

```bash
kubectl -n "$NS" get pvc,pods -o wide
kubectl -n "$NS" describe pvc block-test
kubectl -n "$NS" describe pvc fs-test
kubectl -n "$NS" get events --sort-by=.metadata.creationTimestamp
kubectl -n rook-ceph get pods -o wide
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph health detail
```

进一步 describe 对应失败 Pod。将 namespace、卡住的命令、Pod/PVC 状态、事件及 Ceph 告警发回即可；不要把 Secret 完整输出发回。

默认保留测试 PVC、Pod、S3 用户和桶。两个 StorageClass 的 reclaimPolicy 都是 Delete；删除 PVC 会删除对应存储卷，清理时必须明确选中自己的测试资源，不能删 rook-ceph namespace 或 CephCluster。

状态检查和手测命令均针对 .20～.22；不涉及 .10～.12 的另一套平台。
