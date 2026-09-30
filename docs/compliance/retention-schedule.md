# Retention schedule

Version 2026-10-01. This is MistWarp's written data retention policy for Classroom, as required by COPPA (16 CFR 312.10), and is summarised in the public notice at `/classroom/privacy`.

| Data | Kept until | How it is deleted |
| --- | --- | --- |
| Student account, hashed secret, sign-in data | The teacher deletes the student, the class is deleted, or the student is moved to their own account | `deleteStudentAccount`, straight away |
| Student projects and history | As above | Project records straight away. Files in R2 once no other project references them |
| Submissions, feedback, grades | As above, or when the assignment or class is deleted | Straight away |
| Pending release links | 14 days, or when used, cancelled or the student is deleted | Straight away |
| Class, assignments, groups, presentations, activity log | The teacher deletes the class, or 12 months without a teacher opening it | `purgeClassroomClass` |
| Any class with no teacher visit for 11 months | Teachers are warned, and the class is deleted at 12 months, never less than 14 days after the warning | `sweepClassroomRetention`, every 6 hours |
| All of a school's or teacher's Classroom data, on request | Within 30 days of a request to privacy@mistwarp.org | `POST /v1/admin/classroom/purge` with `confirm: "delete"` |
| Editor error reports | 90 days, and at most the newest 1,000. Reports from student sessions never include the username | `pruneSiteErrors`, whenever a report arrives |
| IP addresses | Not stored for signed-in users. Held in memory for rate limiting only | Not applicable |
| Terms acceptance records, billing records | 6 years after the last use or payment | Manual yearly review |

Backups: there are currently no backups of the database, so deleted records do not survive anywhere. When backups are added they must expire within 30 days, and this schedule and the public notice must say so.

## Handling a deletion request

1. Confirm the request comes from the school, or from a parent whose request the school has confirmed.
2. Find the teacher's username or the school ID.
3. Download a copy for the school first if they want one.
4. Run the purge route, and reply with the number of classes and students deleted and the date.
5. Record the request and the date it was completed.
