# CMS esMD X12N 278 samples

These 278 requests and responses are the sample transactions in the CMS Electronic Submission of Medical Documentation (esMD)
X12N 278 Companion Guide (https://www.cms.gov/files/document/esmd-x12n-278-companion-guide.pdf), a work of the US government.
Each was copied from the guide's text with its layout removed (line breaks and page furniture), and the ISA's fixed-width fields
padded where the PDF's text had lost their spaces. Nothing else was changed, including the guide's own inconsistencies.

| File | The guide's sample |
|---|---|
| request-urgent.x12 | Prior authorization request, level of service U (urgent) |
| response-pended.x12 | Response pended (HCR A4, reason 0U) |
| response-certified.x12 | Response certified (HCR A1, UTN567) |
| response-denied.x12 | Response not certified (HCR A3, reason 13) |
| response-modified.x12 | Response modified (HCR A6, UTN3456) |
| response-cancelled.x12 | Response cancelled (HCR C, reason 0P) |
| response-rejected.x12 | Response rejected (AAA, request not considered) |
| response-lines.x12 | Response with two service lines, each certified |
| ack-999-rejected.x12 | 999 rejecting a 278 (IK3 N4, IK4 element 3 code 7) |
