# Releasing CricketStream

How the Go edition (v3) gets published beside the Python edition (v2.x) without
disturbing the clubs using v2, and how its Windows builds get signed. Everything here is
done by a maintainer; the release workflow does the building.

## The plan in one paragraph

One repository, two lines. The Python edition moves to a `python` branch — its releases
(v2.12 and earlier) stay downloadable and it can still get fixes. The Go edition becomes
`main` and is released as **pre-releases** (`v3.0.0-beta.1`, `-beta.2`, …) until it has
been through real matches. GitHub never counts a pre-release as "latest", so neither
edition's update check offers a beta to anyone. When v3 has proved itself, tag `v3.0.0`:
it becomes "latest", and the Python edition starts telling its clubs a new version is out.

**Version tags:** `vMAJOR.MINOR.PATCH`, and for pre-releases only `-beta.N`
(`v3.0.0-beta.1`). The update check compares numbers, and a release beats its own betas,
but it doesn't read words — don't mix in `-rc` or `-alpha`.

## 1. Put the Go edition on GitHub (once)

This repository's history and the Python repository's history are separate. Join them
without losing either — nothing is force-pushed:

```bash
# In this (Go) repository
git remote add origin https://github.com/BridestoweBelstoneCC/Cricket-Live-Stream.git
git fetch origin

# Keep the Python edition on its own branch
git push origin origin/main:refs/heads/python

# Make the Go edition the next commit on main, with both histories kept
git checkout -b go-edition
git merge origin/main --allow-unrelated-histories -s ours \
  -m "Go edition replaces the Python code on main; the Python edition continues on the python branch"
git push origin go-edition
```

Then open a pull request from `go-edition` to `main` on GitHub and merge it. (`-s ours`
keeps the Go tree exactly; the Python files remain in history and on `python`.)

On GitHub, add a line to the `python` branch's README pointing to v3, and in **Settings →
Branches** protect `main` (pull requests and reviews — SignPath Foundation asks for
reviewed changes).

## 2. The first beta (unsigned)

```bash
git tag v3.0.0-beta.1
git push origin v3.0.0-beta.1
```

The **Build executables** workflow builds Windows (x64, Arm), macOS (universal) and Linux,
smoke-tests each, and publishes a **pre-release** with all four. Until signing is set up,
the Windows files are unsigned and the release notes say so (SmartScreen: More info → Run
anyway).

## 3. Apply to SignPath Foundation (once)

SignPath signs open-source projects for free; the certificate is in **SignPath
Foundation's** name, so Windows shows that as the publisher. Their conditions are at
<https://signpath.org/terms> — the project already meets the code-side ones:

- [x] OSI licence (GPL-3.0), no proprietary parts
- [x] Built only by GitHub Actions on GitHub-hosted runners, from this repository
- [x] File metadata: product name `CricketStream`, product version = the tag
      (enforced by `.signpath/artifact-configuration.xml`)
- [x] Code signing policy and privacy statement on the home page (README) and download
      page (`docs/index.html` footer, and every release's notes)
- [x] Says what it changes on the computer, and how to remove it (README)
- [ ] **Fill in the team table** under *Code signing policy* in the README — GitHub
      usernames of committers/reviewers and of approvers
- [ ] Two-factor sign-in on GitHub and SignPath for everyone in that table
- [ ] An existing release (step 2)

Apply at <https://signpath.org> (Apply for an OSS certificate) with the repository and
release links.

## 4. Switch signing on (once approved)

In SignPath:

1. Create the project (slug e.g. `CricketStream`) and add the predefined **GitHub.com**
   trusted build system to it; install the SignPath GitHub App on the repository.
2. **Artifact configuration:** paste `.signpath/artifact-configuration.xml`.
3. **Signing policy** (e.g. `release-signing`) using the SignPath Foundation certificate,
   with the approvers from the README table. Every request waits for one of them.
4. An **API token** for a user with *submitter* rights on that project and policy.

In GitHub, **Settings → Secrets and variables → Actions**:

| Kind | Name | Value |
|---|---|---|
| Variable | `SIGNPATH_ORGANIZATION_ID` | from SignPath |
| Variable | `SIGNPATH_PROJECT_SLUG` | e.g. `CricketStream` |
| Variable | `SIGNPATH_SIGNING_POLICY_SLUG` | e.g. `release-signing` |
| Secret | `SIGNPATH_API_TOKEN` | the API token |

From the next tag on, the workflow's **sign-windows** job sends both Windows builds to
SignPath and **waits for an approver** (approve it in SignPath — the job waits up to a
day). It then checks both signatures are valid, runs the signed build, and only then
publishes the release with the signed files.

## 5. Each release after that

1. `go test ./...` green, `CHANGELOG.md` has the version's section.
2. Rehearse: a practice match on a real streaming laptop with OBS.
3. `git tag v3.0.0-beta.N && git push origin v3.0.0-beta.N`, approve the signing request.
4. Download the Windows build from the release, double-click it on a clean machine: the
   publisher shows *SignPath Foundation*, and no "Windows protected your PC" box once the
   certificate has built reputation (new certificates can still see it for a while).

When v3 has had its matches: tag `v3.0.0` (no `-beta`). It becomes "latest" — clubs on v2
are told about it, clubs on a v3 beta too.
