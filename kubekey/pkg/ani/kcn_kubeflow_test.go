package ani

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestKubeflowKCNTestEnvironmentCapability(t *testing.T) {
	c := kubeflowTestConfig()
	c.Network.Stack = "kcn"
	if err := Validate(c); err == nil {
		t.Fatal("KCN Kubeflow must require the explicit test-environment capability contract")
	}
	if err := yaml.Unmarshal([]byte("networkPolicy: kcn-test-unsupported-v1\n"), c.Kubeflow); err != nil {
		t.Fatal(err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("accepted KCN test environment cannot reach the first-install chain: %v", err)
	}
	spec := kubeflowSpec(c)
	if spec["network_stack"] != "kcn" || spec["network_policy"] != "unsupported" || spec["network_policy_contract"] != "kcn-test-unsupported-v1" {
		t.Fatalf("runtime capability differs from selected provider: %v", spec)
	}
	c.Network.Stack = "kubeovn"
	if err := Validate(c); err == nil {
		t.Fatal("KCN unsupported acknowledgement must not weaken Kube-OVN isolation")
	}
}

func TestKubeflowKubeOVNCapabilityRemainsRequired(t *testing.T) {
	c := kubeflowTestConfig()
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
	spec := kubeflowSpec(c)
	if spec["network_stack"] != "kubeovn" || spec["network_policy"] != "required" || spec["network_policy_contract"] != "kubeovn-required-v1" {
		t.Fatalf("Kube-OVN isolation contract absent: %v", spec)
	}
}
