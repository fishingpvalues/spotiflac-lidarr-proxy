# Contributing

## Before you start

Small fixes: just open a PR. Anything larger: open an issue first, because
this project deliberately says no to a lot - see "Scope" below.

## Ground rules

- **Conventional Commits are enforced** by the `commit-msg` hook, and they
  decide the next version. `fix:` is a patch, `feat:` is a patch while we are
  pre-1.0, `feat!:` or a `BREAKING CHANGE:` footer is a minor. release-please
  turns them into the changelog, so the commit subject is user-facing text -
  write it for someone reading release notes, not for yourself.
- **Never bump `version.txt` or create a `v*` tag.** The bot owns both.
- `make test` and `make lint` must pass. `make hooks-install` wires the hooks.
- A bug fix wants a test that fails without it. The tests in this repo exist
  mostly to pin things that already went wrong once in production; that is
  the bar.

## Comments

Explain *why*, and prefer a measurement to an assertion. Most of the
non-obvious code here carries the observation that forced it - a date, a
count, what the log actually said. If you are changing such code, update the
reasoning rather than deleting it.

## Scope

Things this project intentionally does not do:

- work around upstream SpotiFLAC bugs by patching installed site-packages -
  it is lost on every version bump and hides the problem meanwhile;
- add providers that need credentials this proxy would have to store;
- grow a UI. Lidarr is the UI.

## Legal

Contributions must not add functionality whose purpose is circumventing
access controls or redistributing content. See the README's Legal section.
