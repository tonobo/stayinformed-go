# Authentik protection for webcal

Webcal should use two independent credentials:

1. The client authenticates to Authentik with an Authentik app password.
2. The token sidecar authenticates to Stay Informed and gives webcal only a short-lived access token through a memory-backed file.

Do not pass the incoming `Authorization` header to Stay Informed. Authentik owns that header and removes valid Basic credentials before forwarding the request.

## Required Authentik mode

Use an Authentik proxy provider in single-application forward-auth mode with **Intercept header authentication** enabled. Authentik documents that received HTTP Basic credentials must use an app password. It also recommends persisting the cookies returned by the outpost to avoid authenticating every calendar refresh.

The generic Helm chart does not create an Authentik provider or outpost because those objects belong to the identity platform lifecycle. Configure the HTTPRoute labels and parent references for the cluster's forward-auth policy through `webcal.httpRoute` values.

A native gateway OIDC redirect filter is not sufficient for WebCal clients that can only send HTTP Basic credentials. Browser access can use the normal Authentik flow through the same proxy provider.

References:

- [Authentik header authentication](https://docs.goauthentik.io/add-secure-apps/providers/proxy/header_authentication/)
- [Authentik Envoy forward-auth configuration](https://docs.goauthentik.io/add-secure-apps/providers/proxy/server_envoy/)
