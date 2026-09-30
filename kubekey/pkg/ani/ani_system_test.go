package ani

import (
	"path/filepath"
	"testing"
)

func TestANISystemSelection(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		c := validConfig()
		c.ANISystem = ANISystem{Enabled: enabled, PackageRoot: "/opt/ani/application"}
		if err := Validate(c); err != nil {
			t.Fatal(err)
		}
		spec, err := KubeKeyConfig(c, "/opt/ani/artifact.tgz", "/opt/ani/artifact", testImageTable())
		if err != nil {
			t.Fatal(err)
		}
		selection, _, err := ANIPlaybookSelection(filepath.Join("..", "..", "builtin", "core", "playbooks", "create_cluster.yaml"), spec)
		if err != nil {
			t.Fatal(err)
		}
		if selection["system"] != enabled || aniRoleEnabled("system", c) != enabled {
			t.Fatalf("application role selection differs from enabled=%t", enabled)
		}
	}
}

func TestANISystemRequiresExplicitPackage(t *testing.T) {
	for _, path := range []string{"", "relative", "/tmp/quote'", "/tmp/line\n"} {
		c := validConfig()
		c.ANISystem = ANISystem{Enabled: true, PackageRoot: path}
		if err := Validate(c); err == nil {
			t.Fatalf("accepted application package path %q", path)
		}
	}
	c := validConfig()
	c.Profile = "base"
	c.ANISystem = ANISystem{Enabled: true, PackageRoot: "/opt/ani/application"}
	if err := Validate(c); err == nil {
		t.Fatal("accepted application install in the base profile")
	}
}
