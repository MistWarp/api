# Information security programme

Version 2026-10-01. This is MistWarp's written information security programme for Classroom, as required by COPPA (16 CFR 312.8) and to meet UK GDPR Article 32.

## Responsible person

Sophie, who runs MistWarp, is responsible for this programme, for security decisions and for responding to incidents.

## Risk assessment

Security risks are assessed in the [DPIA](dpia.md), at least once a year and after significant changes. Each review checks the list below and records the result in the table at the end.

## Safeguards in place

**Access control**

- Students reach only an allowlist of API routes (`classroom-policy.osl`, `classroomAccessGuard`), and only projects they have a role on.
- Every teacher route checks that the user teaches the class (`taughtClassOrFail`). School admin routes check school admin status.
- Admin routes need an account in `ADMIN_USERS`. Only the operator is an admin.

**Authentication**

- Student passwords and picture sequences are stored as bcrypt hashes (cost 10).
- 5 failed attempts lock a student account for 15 minutes. Sign-in routes are rate limited per IP.
- Teachers sign in through Rotur. Sessions are bearer tokens stored on the server.

**Encryption**

- All traffic uses TLS through Cloudflare.

**Data minimisation and retention**

- See the [retention schedule](retention-schedule.md). Automatic deletion runs every 6 hours (`classroomRetentionLoop`).

**Third parties**

Student sessions do not send analytics and do not connect to cloud variable servers, custom or unsandboxed extensions, extension network requests, git CORS proxies, Rotur avatars, Google Fonts, or third-party TURN relays. Classroom notifications stay inside MistWarp and are never pushed to Rotur.

**Logging**

- Teacher changes to a class are written to the class activity log.
- The server's request log records method, path, status and timing, not IP addresses or bodies.

**Infrastructure**

- The API runs on hardware the operator controls in the UK. Project files are in Cloudflare R2.

## Known gaps and actions

| Gap | Action | Target |
| --- | --- | --- |
| The SQLite database and `data/*.json` are not backed up to a second location. Losing the server would lose accounts, classes and submissions (project files survive in R2). | Nightly encrypted copy of `data/` to a separate R2 bucket or another provider, kept 30 days, with a restore tested every 6 months. Update the retention schedule with the 30 day backup window. | Before selling School plans |
| No written record of server hardening (disk encryption, OS updates, SSH keys). | Record the current settings here and review them yearly. | Next review |
| The R2 bucket must only expose `assets/` publicly. | Check the bucket's public access settings and record the result here. | Next review |

## Testing and monitoring

- Unit tests cover the student route allowlist, terms, retention and trial rules (`osl test`).
- Each review includes a manual check that a student account cannot open a project, profile or community page outside its class.

## Reviews

| Date | By | Result |
| --- | --- | --- |
| 2026-10-01 | Sophie | First version |
