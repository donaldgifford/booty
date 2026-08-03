// Package httpsrv is booty's stdlib HTTP serving core.
//
// It routes the whole boot-time HTTP surface, using only [net/http]:
//
//   - GET /healthz, GET /readyz — liveness and readiness probes.
//   - GET /boot.ipxe — the chainload script that collects machine identity.
//   - GET /ipxe — the per-machine iPXE boot script.
//   - GET /machine-config — the Talos machineconfig.
//   - GET /cloud-init/{meta-data,user-data,vendor-data} — cloud-init NoCloud.
//   - POST /proxmox/answer — the Proxmox automated-installer answer file.
//   - GET /boot/{path...} — kernels, initrds, and other boot assets.
//
// Routing is dependency-gated: each route is registered only when [Options]
// carries what it needs — Catalog and Renderer enable the script and config
// endpoints, BootDir enables asset serving. Anything left zero simply has no
// route, so a partially configured server starts and serves what it can. The
// health endpoints are always registered.
//
// The package is named httpsrv rather than http so it never shadows [net/http]
// inside its own files.
//
// # Usage
//
// Wire a catalog and renderer into a server and serve:
//
//	srv := httpsrv.New(httpsrv.Options{
//		Catalog:  cat,
//		Renderer: renderer,
//		BootDir:  "/var/lib/booty/boot",
//		BaseURL:  "http://192.168.1.10:8080",
//	})
//	if err := srv.ListenAndServe(ctx, ":8080"); err != nil {
//		return err
//	}
//
// [Server.Handler] returns the underlying [net/http.Handler] instead, for
// callers that own their own server or want to wrap it in middleware.
//
// Ground-up walkthrough:
// https://github.com/donaldgifford/booty/blob/main/docs/go-ipxe/06-http-server-stdlib.md
package httpsrv
