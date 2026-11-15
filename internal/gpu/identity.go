package gpu

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// SysfsDRM is the drm class directory. Tests point it at a fake tree.
var SysfsDRM = "/sys/class/drm"

// Identity is a render node's marketed name and PCI address.
type Identity struct {
	Node string
	Name string
	PCI  string
	ID   string
}

// Identify reads the PCI device behind a render node. CPU and NVENC
// indexes, and nodes with no sysfs entry, come back with only Node set.
func Identify(path string) Identity {
	return identify(SysfsDRM, path)
}

func identify(root, path string) Identity {
	node := nodeName(path)
	id := Identity{Node: node}
	if !strings.HasPrefix(node, "renderD") {
		return id
	}
	dev := filepath.Join(root, node, "device")
	if target, err := os.Readlink(dev); err == nil {
		id.PCI = pciFromLink(target)
	}
	vendor, vok := readHex16(filepath.Join(dev, "vendor"))
	device, dok := readHex16(filepath.Join(dev, "device"))
	if !vok || !dok {
		return id
	}
	id.ID = fmt.Sprintf("%04x:%04x", vendor, device)
	if vendor == 0x8086 {
		id.Name = arcName(device)
	}
	return id
}

func nodeName(path string) string {
	if path == "" || path == "cpu" {
		return path
	}
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) {
		return path
	}
	return base
}

var bdfRE = regexp.MustCompile(`(?i)[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]`)

func pciFromLink(target string) string {
	all := bdfRE.FindAllString(target, -1)
	if len(all) == 0 {
		return ""
	}
	return strings.ToLower(all[len(all)-1])
}

func readHex16(path string) (uint16, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(b))
	s = strings.TrimPrefix(strings.ToLower(s), "0x")
	n, err := strconv.ParseUint(s, 16, 16)
	if err != nil {
		return 0, false
	}
	return uint16(n), true
}

// arcNames maps Intel PCI device ids to the Arc product name.
// Ids Mesa still labels only as "Intel Graphics" are omitted on purpose.
var arcNames = map[uint16]string{
	0x5690: "Arc A770M",
	0x5691: "Arc A730M",
	0x5692: "Arc A550M",
	0x5693: "Arc A370M",
	0x5694: "Arc A350M",
	0x5696: "Arc A570M",
	0x5697: "Arc A530M",
	0x56a0: "Arc A770",
	0x56a1: "Arc A750",
	0x56a2: "Arc A580",
	0x56a5: "Arc A380",
	0x56a6: "Arc A310",
	0x56b0: "Arc Pro A30M",
	0x56b1: "Arc Pro A40/A50",
	0x56b2: "Arc Pro A60M",
	0x56b3: "Arc Pro A60",
	0x56ba: "Arc A380E",
	0xe20b: "Arc B580",
	0xe20c: "Arc B570",
	0xe211: "Arc Pro B60",
	0xe212: "Arc Pro B50",
}

func arcName(device uint16) string {
	return arcNames[device]
}
