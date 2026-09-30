# Classroom compliance records

These are MistWarp's own records for MistWarp Classroom. They are the documents a school, the Information Commissioner's Office or the US Federal Trade Commission can ask to see. The public documents schools agree to live on the site: `/classroom/terms`, `/classroom/dpa`, `/classroom/privacy` and `/classroom/subprocessors` (in scratch-gui `src/community/legal/`).

| Record | Why it is needed |
| --- | --- |
| [Data protection impact assessment](dpia.md) | UK GDPR Article 35 and standard 2 of the ICO Children's Code |
| [Record of processing](record-of-processing.md) | UK GDPR Article 30(2) for a processor |
| [Information security programme](security-programme.md) | UK GDPR Article 32, and COPPA 16 CFR 312.8 |
| [Retention schedule](retention-schedule.md) | UK GDPR Article 5(1)(e), and COPPA 16 CFR 312.10 |
| [Breach response procedure](breach-response.md) | UK GDPR Articles 33 and 34, and the 48 hour promise in the Data Processing Agreement |

Owner: Sophie, sole trader trading as MistWarp. Contact: privacy@mistwarp.org.

Review all five at least once a year, after any significant change to Classroom, and after any breach. Record each review in the table at the end of each file.

## Open actions

- Register with the ICO and pay the data protection fee, then add the registration number to `LEGAL.icoRegistration` in scratch-gui `src/community/legal/config.js`.
- Add a postal address for notices to `LEGAL.postalAddress`. COPPA requires the operator's address in the notice. A service address or PO box is fine.
- Make sure privacy@mistwarp.org receives mail and is checked at least every two working days.
- Accept Cloudflare's Data Processing Addendum in the Cloudflare dashboard if it has not been accepted already, and keep a copy.
- Back up the API's `data/` directory (the SQLite database and JSON files) to a second location. See the security programme.
- Have a UK solicitor review the Classroom Terms and Data Processing Agreement before selling School plans.
