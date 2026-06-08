---
"@ccfly/react": patch
---

fix(compose): reliable send-button feedback + image send

- Instant press feedback (pointer-driven `.is-pressed`) — `.cbtn.primary` overrode
  `.cbtn:active` and the disabled state had no styling at all, so a tap showed nothing.
- Not-sendable state is now visible (`.is-off`) and still tappable, flashing the reason
  (uploading / busy / not ready) instead of silently doing nothing.
- Long-press with an image but no text now sends the image instead of dropping it.
