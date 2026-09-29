package ani

import (
	"reflect"
	"strings"
	"testing"
)

func TestComponentsScopeKeepsRoleManagedDependenciesInsideTheirOwner(t *testing.T) {
	site := strings.Replace(r06SiteA, "  certManager: {enabled: true}",
		"  certManager: {enabled: true}\n"+
			"  kubevirt: {enabled: true, vmNode: node2, storageClass: ani-block, scratchStorageClass: ani-block, storageSize: 2Gi}\n"+
			"  harbor: {enabled: true, externalAddress: 192.0.2.11, storageClass: ani-block, storageSize: 10Gi}", 1)
	cluster, err := ParseClusterConfig([]byte(site))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(cluster); err != nil {
		t.Fatal(err)
	}
	scope, err := componentsScope(cluster, []string{"kubevirt", "harbor"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"kubevirt", "harbor"}; !reflect.DeepEqual(scope, want) {
		t.Fatalf("CDI and Harbor dedicated services belong to their owner roles: scope=%v, want %v", scope, want)
	}
}
