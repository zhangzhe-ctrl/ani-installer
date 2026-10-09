package ani

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func kcnSiteSpec(c ClusterConfig) map[string]any {
	data, _ := json.Marshal(struct {
		Network Network
		Nodes   []NodeConfig
	}{c.Network, c.Nodes})
	checksum := sha256.Sum256(data)
	return map[string]any{
		"managedDevices":   c.Network.KCN.ManagedDevices,
		"encapNetworks":    c.Network.KCN.EncapNetworks,
		"intranetNetworks": c.Network.KCN.IntranetNetworks,
		"configChecksum":   hex.EncodeToString(checksum[:]),
	}
}
