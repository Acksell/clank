// Package daytona launches and tears down a clank-host running inside
// a Daytona sandbox, returning a *hostclient.HTTP that the Hub can
// register alongside the local host.
//
// The transport is Daytona's "Preview URL" feature: every TCP port a
// sandbox listens on is exposed at
// https://{port}-{sandboxId}.proxy.daytona.work, authenticated by a
// per-sandbox token sent as the x-daytona-preview-token header. The
// proxy terminates TLS, so clank-host inside the sandbox speaks plain
// HTTP and binds 0.0.0.0:{port} (the network boundary is the security
// perimeter — see --allow-public on cmd/clank-host).
//
// Lifecycle:
//
//	Launch(ctx, opts) → Handle{Stop()}
//	   1. cross-compile clank-host for linux/arm64 (cached by SHA)
//	   2. daytona.NewClientWithConfig + Create sandbox (default snapshot)
//	   3. UploadFile binary into sandbox
//	   4. Process.CreateSession + ExecuteSessionCommand(runAsync=true)
//	   5. GetPreviewLink → (URL, token)
//	   6. poll {url}/status until 200 (5s cap)
//	   7. wrap as hostclient.NewRemoteHTTP and return
//
// Handle.Stop() deletes the sandbox; the running clank-host process
// dies with it, no graceful-shutdown needed.
package daytona
