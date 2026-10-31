---
status: "accepted"
summary: "Use a Desktop OAuth client and copy the complete loopback redirect URL back to a headless gohealthcli process."
read_when:
  - "Designing or implementing headless Google OAuth completion."
  - "Changing OAuth client types or redirect handling."
  - "Operating gohealthcli on a host without a local browser."
---
# Headless OAuth Uses a Copied Loopback Redirect

Issue #387 selects the Google Desktop OAuth client and its dynamic IPv4
loopback redirect, with an operator copying the complete redirected URL from a
browser back to the headless `gohealthcli` process. This is a manual delivery
of the standard loopback response, not Google's removed out-of-band flow and
not permission to accept a bare authorization code.

## Support status

Google's installed-app documentation recommends a random-port loopback IP
redirect for macOS, Linux, and Windows Desktop clients. It requires PKCE, an
exact redirect URI, and CSRF protection such as `state`. Google's loopback
migration guide says the flow remains supported for Desktop clients. Copying
the complete loopback URL from an unreachable callback page between two
machines is an operator technique around that supported redirect; Google does
not document it as a distinct OAuth mode. The live proof below establishes that
Google accepts the redirect and the built CLI completes it successfully.

Google Health's setup page currently tells new integrations to create a Web
Server client with an HTTPS redirect. That shape is not selected: it would need
a registered HTTPS callback service, accept a Web-client secret model, and
expand gohealthcli beyond its local-only boundary. The current CLI intentionally
accepts only Desktop-client JSON.

An SSH-forwarded loopback port is a compatible fallback for an operator who can
maintain a trusted tunnel and forward the browser machine's exact local port to
the headless listener. It is not the default because dynamic port discovery and
tunnel setup add operational coupling. It also does not remove the need for
PKCE, exact `state` validation, or one-time completion.

## Operator contract

1. Start authorization on the headless host. The CLI creates a fresh PKCE
   verifier and random `state`, retains them in the configured Credential Store,
   and prints the Google authorization URL. The verifier never leaves the
   Credential Store; `state` is necessarily present in the URL and is handled
   as sensitive transfer material with the authorization code.
2. Open that URL in a trusted system browser and complete consent.
3. When the browser reaches the unavailable loopback page, copy the entire
   address-bar URL, including `code` and `state`. Do not copy only the code.
4. Supply that URL to the completion command over stdin, not as a command-line
   argument. The CLI must validate the exact scheme, loopback host, port, path,
   and `state` before exchanging the code with the original PKCE verifier.
5. The CLI records Google's returned scope set, calls `users.getIdentity`,
   enforces the Health Archive's existing Google Identity, and consumes the
   pending authorization exactly once.

The redirected URL is short-lived bearer material. Keep it out of shell
history, logs, terminals captured by automation, chat, screenshots, issue and
pull-request bodies, and repository artifacts. Use this flow only across
operator-controlled hosts and a trusted transfer path. Prefer ordinary
`gohealthcli connect` whenever the browser and CLI share a loopback interface.

## Evidence

Non-secret validation on 2026-08-03 confirmed that the built CLI produces a
Google HTTPS authorization request with a Desktop-client dynamic IPv4 loopback
redirect, a non-empty random `state`, and PKCE `code_challenge_method=S256`.
Repository tests cover the PKCE transform, exact `state` callback check, token
exchange form, returned-scope parsing, identity verification, and non-success
token exchange handling.

An isolated live run on 2026-08-03 then proved the complete flow without a
Provider health-data request:

- the authorization request used PKCE S256 and a random `state`;
- the copied complete loopback URL contained a code and the exact expected
  `state`, which the copy boundary and built CLI listener validated before
  exchange;
- the built CLI exchanged the code with its originating verifier and Google
  returned five scopes, including every required base read-only scope;
- `users.getIdentity` succeeded with the expected identifier-field shape;
- resubmitting the consumed code with the same client and redirect tuple
  returned HTTP 400 `invalid_grant`.

The proof used an isolated Health Archive and owner-only files. Only the
assertions above were retained; authorization URLs, codes, state, tokens,
client details, identity values, and screenshots were not published. The
production Health Archive was not opened for writes.

## Implementation boundary

Issue #389 implements the start/completion flags, ten-minute Credential Store
state, exact redirect parsing, atomic single-use claiming, granted-scope
archival, and concurrency tests. It does not change interactive `connect`, add
a Provider scope, permit health-data requests, accept a bare authorization
code, or store pending authorization in the Health Archive.

## Sources

- Google OAuth 2.0 for iOS and Desktop apps:
  https://developers.google.com/identity/protocols/oauth2/native-app
- Google loopback IP migration guide:
  https://developers.google.com/identity/protocols/oauth2/resources/loopback-migration
- Google Health API OAuth setup:
  https://developers.google.com/health/setup
- Google Health `users.getIdentity`:
  https://developers.google.com/health/reference/rest/v4/users/getIdentity
