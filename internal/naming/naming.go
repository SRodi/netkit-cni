package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const LinuxInterfaceNameMax = 15

func HostInterface(containerID string) (string, error) {
	if containerID == "" {
		return "", fmt.Errorf("container ID is required")
	}
	sum := sha256.Sum256([]byte(containerID))
	return "nkh" + hex.EncodeToString(sum[:6]), nil
}
