package ani

import "fmt"

const (
	KServeLLMVersion             = "v0.17.0"
	KServeLLMImageReference      = "docker.io/kserve/llmisvc-controller:v0.17.0"
	KServeLLMAMD64ManifestDigest = "sha256:30795379cceea64653daba008fee9b054bb889523000b53586cd3bdc48608b79"
	KServeLLMSourceIndexDigest   = "sha256:f9ab82e15ba714eb6a734c8e20ef42e0dabdaeb88aa56848d1f71137c2272a0e"
	KServeLLMCRDChartPath        = "charts/kserve-llmisvc-crd/v0.17.0.tgz"
	KServeLLMCRDChartSHA256      = "e49aee5322397d4dacd9ee9eadf98b797e8f726e1e8c21acadc2854d9e27aa81"
	KServeLLMChartPath           = "charts/kserve-llmisvc-resources/v0.17.0.tgz"
	KServeLLMChartSHA256         = "456fd2cc244c344a262ca7d553092e6abb3e8cb2c3b87f12653cf46ad3fbffba"
)

func requireKServeAdditionBase(base RunManifest, scope []string) error {
	for _, name := range scope {
		if name == "kserve" && (base.Kubeflow == nil || !base.Kubeflow.Enabled || (base.Kubeflow.Release != KubeflowRelease && base.Kubeflow.Release != KubeflowLLMRelease)) {
			return fmt.Errorf("KServe LLM addition requires the original successful Kubeflow Standard base; it cannot install or replace Standard incrementally")
		}
	}
	return nil
}
