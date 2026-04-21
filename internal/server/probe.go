// Probe builder — bridges the auth package's ProbeFunc contract to the
// concrete client + langserver + proxy stack that only the server layer
// sees. Used by both the dashboard "probe now" button and the 6-hour
// background loop.
package server

import (
	"context"
	"errors"
	"time"

	"windsurfapi/internal/auth"
	"windsurfapi/internal/client"
	"windsurfapi/internal/langserver"
	"windsurfapi/internal/models"
	"windsurfapi/internal/proxycfg"
)

// MakeProbeFunc returns an auth.ProbeFunc closed over d. Sends a single "hi"
// turn for each canary model against the LS instance that matches the
// account's current proxy.
func (d *Deps) MakeProbeFunc() auth.ProbeFunc {
	return func(ctx context.Context, apiKey, modelKey string) error {
		info := models.Get(modelKey)
		if info == nil {
			return errors.New("unknown model")
		}
		accountID := ""
		for _, a := range d.Pool.All() {
			if a.APIKey == apiKey {
				accountID = a.ID
				break
			}
		}
		var px *langserver.Proxy
		if accountID != "" {
			px = proxycfg.Effective(accountID)
		}
		lsCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		entry, err := d.LSP.Ensure(lsCtx, px)
		if err != nil {
			return err
		}
		cli := client.New(apiKey, entry)
		defer cli.Close()

		probeCtx, pcancel := context.WithTimeout(ctx, 30*time.Second)
		defer pcancel()

		msgs := []client.ChatMsg{{Role: "user", Content: "hi"}}
		if info.ModelUID != "" {
			_, err = cli.CascadeChat(probeCtx, msgs, info.Enum, info.ModelUID, client.CascadeOptions{})
		} else {
			_, err = cli.RawChat(probeCtx, msgs, info.Enum, info.ModelUID, nil)
		}
		return err
	}
}

// ProxyResolver returns the concrete proxy resolver the auth package wants.
// Defined here so main.go can pass it in without importing proxycfg twice.
func ProxyResolver() func(string) *langserver.Proxy {
	return func(id string) *langserver.Proxy { return proxycfg.Effective(id) }
}
