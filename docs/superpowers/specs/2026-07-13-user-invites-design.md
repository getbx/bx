# User Invite Design

## Goal

Let a non-technical person join bx from an invitation without seeing a server
address, transport name, terminal command, or raw `bx://` configuration.

## User Flow

1. An administrator creates an invite for a named person.
2. bx returns a shareable HTTPS invite URL and a QR code for that URL.
3. The recipient opens the URL.
4. If bx is installed, the page opens bx and passes it the invite. If it is not
   installed, the page offers the appropriate macOS or Windows installer, then
   resumes the same invite after installation.
5. bx displays the provider name and asks the recipient to confirm starting
   protection. It validates and stores the underlying client configuration only
   after confirmation.

## Invitation Model

- An invite represents one managed user, not server administration access.
- The externally shared URL contains an opaque, high-entropy token. It does not
  reveal the VPS address, credentials, or underlying transport links.
- Invites are reusable by default so a recipient can reinstall bx or move to a
  new device without asking the administrator to create a new link.
- An administrator can pause, reissue, or revoke an invite at any time. A
  revoked invite cannot be imported again; existing devices follow the server's
  revocation policy on their next authorization check.
- Expiry is an optional administrator policy, not the default. The first
  version favors recoverability for ordinary users.

## Product Boundaries

- `bx://` remains the internal configuration format and CLI-compatible path.
- The ordinary user surface is an HTTPS invite URL or its QR representation.
- The recipient never needs to know whether the connection uses brook, REALITY,
  hysteria2, or a future transport.
- The invite flow does not add billing, bandwidth quotas, organization roles, or
  a general management dashboard. Those need a separate management model.

## Components

### Server

The server stores an invite record keyed by a random token. It maps that token
to the managed user, permitted configuration, lifecycle state, and optional
expiry. A token-resolution endpoint returns only recipient-safe metadata and a
short-lived import payload.

### Invite Page

The public page is a tiny bootstrapper, not an account portal. It recognizes
whether bx can handle an app link, otherwise sends the recipient to the signed
installer for their platform. It never renders raw connection credentials.

### Desktop App

The macOS and Windows apps register a bx app-link handler. They receive the
short-lived import payload, show a concise confirmation screen, run setup with
the imported configuration, and offer to begin protection immediately.

### CLI

Existing `bx user invite`, `bx user list`, and `bx user revoke` remain the
administrator foundation. They evolve to print the HTTPS invite URL and QR
output, while retaining a raw-link escape hatch for automation and offline
usage.

## Security and Failure Handling

- Tokens must be unguessable, scoped to one recipient, and redactable in all
  logs and JSON diagnostics.
- The page fetches no configuration until it validates the token; the App
  verifies the import payload before applying it.
- Opening a revoked, expired, malformed, or already-paused invite produces a
  clear recipient-safe explanation and no partial client configuration.
- Installer handoff must preserve the opaque invite token without placing it in
  system logs where practical. The user can reopen the original invite URL if
  the handoff is interrupted.
- Importing an invite is an explicit user action. Starting whole-device
  protection remains a separate confirmation because it changes network
  behavior.

## Validation

- Server tests cover token creation, redaction, reuse, pause, reissue, revoke,
  expiry, and the absence of transport secrets from recipient metadata.
- App tests cover a valid import, declined confirmation, invalid or revoked
  token, installation handoff, and a completed setup that does not start
  protection until confirmed.
- End-to-end smoke coverage verifies an administrator can invite a user, the
  recipient can import on macOS and Windows, and revocation prevents a later
  import.
