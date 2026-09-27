# B05 KubeVirt/CDI CPU guest

- Namespace: kubevirt and cdi operators, test guest in ani-platform.
- DataVolume, PVC and VM: ani-platform/ani-b05-guest. This is a run-owned persistent test disk; do not delete it as a routine cleanup.
- Guest source: packaged CirrOS 0.6.3 image, SHA256 7d6355852aeb6dbcd191bcda7cd74f1536cfe5cbf8a10495a7283a8396e4b75b, copied into the internal ani-b05-guest HTTP Pod and imported by CDI.
- Guest key and known_hosts: /etc/kubernetes/ani/kubevirt/guest-ssh-key and /etc/kubernetes/ani/kubevirt/known_hosts on the installer node.
- Evidence: {{ .ani.run.logs_dir }}/b05-kubevirt.log; this includes the DataVolume phase, guest command, PVC UID and marker after normal stop/start.
- VM node: {{ (index .ani.components "kubevirt").vm_node }}. The role verifies hardware /dev/kvm before writing any KubeVirt resources. CPU emulation is not enabled.
- The selected storage and scratch StorageClasses must have Immediate binding. WFFC configurations stop at preflight with a specific error.
