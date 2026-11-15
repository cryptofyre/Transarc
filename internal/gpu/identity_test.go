package gpu

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPCIFromLink(t *testing.T) {
	got := pciFromLink("../../devices/pci0000:00/0000:00:03.1/0000:0a:00.0/0000:0b:01.0/0000:0c:00.0")
	if got != "0000:0c:00.0" {
		t.Fatalf("pci %q", got)
	}
	if pciFromLink("../../devices/platform/gpu/drm/renderD128") != "" {
		t.Fatal("platform node looked like a PCI address")
	}
}

func TestArcName(t *testing.T) {
	if arcName(0x56a1) != "Arc A750" || arcName(0x56a0) != "Arc A770" || arcName(0xe20b) != "Arc B580" {
		t.Fatalf("known ids: %q %q %q", arcName(0x56a1), arcName(0x56a0), arcName(0xe20b))
	}
	if arcName(0x56a3) != "" {
		t.Fatalf("unnamed id got %q", arcName(0x56a3))
	}
}

func TestIdentifyReadsSysfs(t *testing.T) {
	root := t.TempDir()
	for _, node := range []string{"renderD128", "renderD129"} {
		dev := filepath.Join(root, node, "device")
		if err := os.MkdirAll(dev, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dev, "vendor"), []byte("0x8086\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dev, "device"), []byte("0x56a1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	id := identify(root, "/dev/dri/renderD128")
	if id.Node != "renderD128" || id.Name != "Arc A750" || id.ID != "8086:56a1" || id.PCI != "" {
		t.Fatalf("%+v", id)
	}
	other := identify(root, "/dev/dri/card0")
	if other.Node != "card0" || other.Name != "" || other.ID != "" {
		t.Fatalf("non-render node %+v", other)
	}
	cpu := identify(root, "cpu")
	if cpu.Node != "cpu" || cpu.Name != "" {
		t.Fatalf("cpu %+v", cpu)
	}
}
