# SECURITY.md process dry run

Date: 2026-10-11. A dry run of the process in [SECURITY.md](https://github.com/polipan/polipan/blob/main/SECURITY.md)
with a pretend report. Nobody outside the team was contacted and nothing was sent or published.

## Pretend report

"Report: the SSO callback uses the `Host` header, so a forged `Host` sends the login code to another site."
(This was a real remark of the M10a review, R9. It is fixed in 1.0.)

## Steps and result

| Step | What we did | Result |
|---|---|---|
| 1. Receive | Checked the two channels of SECURITY.md. | The GitHub "Report a vulnerability" button needs *Private vulnerability reporting* to be turned on in the repository settings. **Check this setting.** The address `security@polipan.dev` must be a real mailbox: no code or doc of the repository can prove it. **Confirm with the owner.** |
| 2. Reply in 3 working days | Wrote the reply text: thanks, the id of the report, who is looking, the next date. | Works. Put the text in a saved reply. |
| 3. Reproduce | Wrote a test first (`TestAC_SSO_*` style) that fails with a forged `Host`. | Works. The test then guards the fix. |
| 4. Fix plan in 14 days | Wrote the plan: smallest fix, test, release note. | Works. |
| 5. Fix on a private branch | A GitHub private fork or a draft security advisory keeps the fix hidden. | The repository has no rule for this. The advisory flow of GitHub is enough. |
| 6. Release | `docs/operations/release.md`: one tag push builds, signs and publishes. | Works. A patch release needs no new step. |
| 7. Advisory and credit | Draft advisory with the affected versions, the fixed version and the reporter name (if they want). | Works. A CVE id is requested from GitHub in the advisory. |

## Gaps found

1. **No disclosure deadline.** SECURITY.md says "we publish a fix and an advisory" but not when. Suggested: 90 days
   after the report at the latest.
2. **No scope.** Say what is in scope (the server, agent, CLI, the images and the install script) and what is not
   (a node where the attacker is already root: see accepted risk AR-4 in the release notes).
3. **No PGP key.** Not needed if GitHub private reporting is on. Say so.
4. **Supported versions** text is for before and after 1.0. Update it when 1.0 is tagged.
5. **Dependency alerts.** `make vuln` and the CI job `vuln` find known problems in dependencies. Turn on Dependabot
   alerts so GitHub also tells the maintainers.

These are suggestions for the owner. SECURITY.md was not changed, because the contact channels are the owner's choice.
