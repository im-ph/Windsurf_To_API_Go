// Tiny indirection so server/server.go can resolve the effective proxy
// without a direct import of proxycfg (which would create a cycle once the
// dashboard layer lands and imports server).
package server

import (
	"windsurfapi/internal/langserver"
	"windsurfapi/internal/proxycfg"
)

func proxycfgEffective(accountID string) *langserver.Proxy {
	return proxycfg.Effective(accountID)
}
