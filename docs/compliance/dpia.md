# Data protection impact assessment: MistWarp Classroom

Version 2026-10-01. Owner: Sophie, who runs Rotur and MistWarp. Next review: 2027-10-01.

## 1. Why a DPIA is needed

Classroom processes personal data about children, including children under 13, for schools in the UK and the US. The ICO Children's Code (standard 2) requires a DPIA for online services likely to be used by children, and UK GDPR Article 35 requires one where processing is likely to be high risk. MistWarp is a processor for schools, and this assessment is offered to schools to support their own DPIAs.

## 2. Description of the processing

- **What:** teacher-created student accounts; sign-in with a class code and a word password or three pictures; projects made in the Scratch-based editor; assignments, submissions, feedback and grades; group projects edited live; presentations to the class; moving a student to their own account; data downloads; automatic deletion after 12 months without a teacher.
- **Data:** student display name (as the teacher types it), generated username, class, bcrypt hash of the password or picture sequence, sign-in times and failed attempts, projects and their files and history, submissions, feedback and grades, group membership, editor settings, notifications, class activity log, editor error reports, and IP addresses in transit.
- **Not collected:** email, phone, address, date of birth, photos, location, Rotur accounts for students.
- **Where:** the API and SQLite database on MistWarp's own hardware in the UK; project files in Cloudflare R2; all traffic through Cloudflare.
- **Who can access it:** the class's teachers and co-teachers; School plan admins; group members for shared projects; the class during a presentation; the operator for administration.
- **Scale:** Free plan up to 5 students per teacher, Classroom plan 35 or more, School plans up to several hundred.

## 3. Necessity and proportionality

- Every data item supports a feature teachers asked for. Names are needed so teachers and students can find accounts. Hashed secrets are needed for sign-in. Projects and submissions are the purpose of the service.
- Data minimisation: no contact details or birth dates exist in the data model. Teachers are told to use first names or nicknames.
- Students cannot publish, comment, follow, chat, message, browse the public community or open projects outside their class, which is enforced on the server by an allowlist of API routes (`classroom-policy.osl`).
- Retention is limited: teachers can delete at any time, classes are deleted after 12 months without a teacher, and schools can ask for deletion within 30 days.
- Lawful basis is the school's (public task for most state schools). In the US the school consents for parents under COPPA for educational use only.

## 4. Risks and measures

| Risk | Likelihood | Severity | Measures | Residual |
| --- | --- | --- | --- | --- |
| A student's work or name is seen by strangers | Remote | Significant | Server-side route allowlist; projects outside the class return 404; no publishing; class code roster shows names only, and teachers are told to use first names and to reset leaked codes | Low |
| Contact between a child and an unknown adult | Remote | Severe | No chat, messages, comments, follows or public profiles for students; room-code live collaboration is blocked for students, and only class members join class sessions | Low |
| Student data reaches third parties without a contract | Possible before this change | Significant | Analytics, cloud variables, custom extensions, extension network access, git remotes, Rotur avatars and third-party TURN servers are blocked for students; classroom notifications are not sent to Rotur; Cloudflare is the only sub-processor | Low |
| Account takeover by guessing a class password | Possible | Moderate | Bcrypt hashes, 5 attempts then a 15 minute lock, per-IP rate limits, teacher reset and disable tools | Low |
| Teacher misuse, such as viewing another class | Remote | Moderate | Every class route checks that the user teaches the class; an activity log records teacher changes | Low |
| Data kept after it is needed | Likely without controls | Moderate | Automatic deletion after 12 months without a teacher, with a warning at 11 months; deletion on request within 30 days | Low |
| Loss of data through hardware failure | Possible | Moderate | Project files are in Cloudflare R2. The database is not yet backed up to a second place: see the security programme action | Medium, until backups exist |
| Breach of the server | Remote | Significant | Admin access limited to the operator; TLS through Cloudflare; hashed secrets; breach procedure with 48 hour notice to schools | Low |
| Transfers outside the UK | Certain for project files at Cloudflare | Low | Cloudflare DPA with the UK International Data Transfer Addendum and the UK Extension to the EU-US Data Privacy Framework | Low |
| Children not understanding what happens to their data | Possible | Minor | Plain-language section for students at the top of `/classroom/privacy` | Low |

## 5. Children's Code standards

- **Best interests, age appropriate application:** Classroom is only for use under a school's supervision, and every student account is treated as a child's.
- **Transparency:** a student-facing summary, and a full notice for parents and schools.
- **Detrimental use, nudge techniques:** no streaks, rewards for time spent, or dark patterns in Classroom.
- **Default settings, data sharing, profiling, geolocation:** highest privacy by default, nothing public, no profiling, no geolocation, no data sharing beyond the sub-processor.
- **Parental controls:** the school, not MistWarp, manages accounts. Parents make requests through the school or privacy@mistwarp.org.
- **Connected toys, online tools:** not applicable. Teachers can download and delete data themselves.

## 6. Conclusion

With the measures above, the residual risk is low, except for availability of the database until backups are in place. Processing can go ahead. Consultation with the ICO under Article 36 is not needed.

## Reviews

| Date | By | Changes |
| --- | --- | --- |
| 2026-10-01 | Sophie | First version |
