---
status: "accepted"
summary: "Use an OS-native Credential Store for runtime OAuth tokens and keep 1Password as an optional setup Secret Provider."
read_when:
  - "Implementing OAuth token storage."
  - "Adding 1Password setup support."
  - "Changing credential storage behavior."
---
# Cross-Platform Credential Store

`gohealthcli` stores runtime OAuth token material in a Credential Store abstraction backed by the OS-native credential service where available: macOS Keychain, Windows Credential Manager, or Linux Secret Service/libsecret. A permission-restricted file is an explicit fallback for development or unsupported environments. 1Password can supply bootstrap secrets such as a Google OAuth client secret, but it is not the default runtime token backend because every-command token access should not depend on password-manager session state.
