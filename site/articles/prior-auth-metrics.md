# Prior authorization metrics under CMS-0057-F: what payers must publish, and how

![A bar chart of prior authorization metrics](docs/assets/articles/hero-metrics.svg)

## In one minute

- A US government rule (CMS-0057-F) makes many health plans publish a yearly report card about **prior authorization**, their system for approving tests and treatments before they happen.
- The report card goes on a public page of the plan's website, so patients, doctors and employers can all see it.
- It must show what needs approval, how often the plan says yes and no, how often it took extra time or an appeal, and how long decisions took.
- The information all comes from records the plan's approval system already keeps. The work is in counting it correctly and clearly.
- Perfuse is free software that reads those records and produces the finished page, laid out the way CMS asks.

> In plain words: once a year, each covered health plan has to post an honest scorecard of how it handled approval requests, and Perfuse builds that scorecard from the plan's own records.

## What this is

Prior authorization is the step where a health plan must agree before a patient gets a particular test, scan, treatment or piece of equipment. It is meant to control cost, but when it is slow or opaque it keeps patients waiting and frustrates doctors.

To shine a light on how plans actually behave, CMS (the Centers for Medicare and Medicaid Services, the government body that runs Medicare and helps run Medicaid) wrote a requirement into its rule CMS-0057-F: covered plans must **publish their prior authorization performance every year** on a public web page. These published numbers are the **metrics**. The idea is transparency. If a plan denies a lot, or takes a long time, anyone can see it.

> In plain words: the metrics are a plain scorecard the plan must post in public, showing how it handled approval requests over the past year.

## Who publishes, and when

The plans covered are the same ones the wider rule covers, which CMS calls **impacted payers** (a payer is simply an insurance plan):

- Medicare Advantage organizations (private plans running Medicare benefits),
- state Medicaid and CHIP fee-for-service programs (public coverage for low-income people and for children),
- Medicaid and CHIP managed care plans (private plans a state hires to run those programs),
- Qualified Health Plan issuers on the federal exchanges (plans sold on HealthCare.gov).

Each of them publishes the report once a year, on a public page of its website. The requirement began with reports covering 2025.

## What the page must show

![What the plan must publish every year](docs/assets/articles/prior-auth-metrics-what.svg)

The page has to answer a set of plain questions. For each one, the plan reports the number:

- **A list of everything that needs approval**, described in plain language, so a patient can see whether their own test or treatment is on it.
- **How often the plan says yes, and how often no**: the share of standard requests approved and the share denied.
- **How often it took extra time**: the share approved only after the plan extended its own deadline to decide.
- **How often an appeal won**: the share of requests approved after the patient appealed a first decision.
- **The same figures for urgent requests**, reported separately from standard ones.
- **How long decisions took**: the average time and the middle time from when a request arrived to when it was decided, for standard and for urgent requests.

Two of those phrases deserve a note. The **average** time is the usual arithmetic average. The **middle** time, which statisticians call the median, is the point where half the requests were faster and half were slower; it is useful because a few very slow cases can pull the average up and make things look worse than they usually are. The plan reports both.

Prescription drugs are left out, because they sit outside the prior authorization parts of the rule. So a row that names a drug does not belong on this page.

> In plain words: the page says what needs approval, how often the plan agreed, how often it dragged its feet or lost an appeal, and how long it all took, with urgent and standard requests kept apart.

## Where the data comes from

Every plan's approval system already records, for each request: when it arrived, whether it was urgent, when it was decided, what the decision was, whether the plan took extra time, and whether the patient appealed and how that turned out. That is everything the metrics need. Nothing new has to be collected.

The real work is in **counting correctly**. Which requests count toward which year? How should a request that was only partly approved be treated? What about a request still waiting when the year ends? And every time a length of time is reported, the unit (hours or days) has to be written out, so a reader is never left guessing whether "3" means three hours or three days.

## What happens, step by step

![Turning records into the public page](docs/assets/articles/prior-auth-metrics-pipeline.svg)

Let us follow a made-up plan. Riverside Health Plan has to publish its report for last year. Its compliance manager, Tom, has never written a line of code and does not want to start now.

1. **The records already exist.** Riverside's approval system has, for every request last year, the date it arrived, whether it was urgent, the date it was decided, the decision, whether extra time was taken, and whether it was appealed.
2. **Tom exports a simple file.** From the approval system, he exports those records as a spreadsheet, one row per request. This is a plain, everyday file; most systems can produce one.
3. **Perfuse reads the file and does the counting.** Tom runs Perfuse, pointing it at the file and telling it the year and the plan's name. Perfuse works out every figure the rule asks for.
4. **Perfuse produces the finished page.** Out comes a ready-to-publish web page, laid out in the shape of CMS's own template, plus the same figures as a spreadsheet and as data for anyone who wants them.
5. **Tom checks and posts it.** Perfuse also tells him plainly if anything looked wrong: rows it could not read, requests still pending that it left out, or a missing list of what needs approval. Tom fixes what he needs to and puts the page on Riverside's website.

What Tom did not have to do: write a program, do the arithmetic by hand, or lay out the page himself.

## Why the counting is the hard part

At first glance, producing these numbers sounds like simple arithmetic: count the yes answers, count the no answers, divide. In practice, the care is in the edge cases, and getting them wrong makes a scorecard that is either misleading or quietly false. A few of the traps:

- **Which year does a request belong to?** A request that arrived in late December but was decided in January belongs, under the template, to the year it was decided in. Count it in the wrong year and two years' reports are both slightly off.
- **What about requests still waiting?** When the year ends, some requests have not been decided yet. They are not approvals and not denials, so counting them as either would distort the picture. They have to be set aside and reported as excluded.
- **Partly approved requests.** Sometimes a plan approves part of what was asked. The template has no separate box for "partly", so these are counted as approvals, and the honest thing is to say how many there were.
- **The unit on every time.** A median faster than a day can round to "0 days", which tells a reader nothing. The template asks that such a figure be written in hours instead. Every length of time needs its unit spelled out.
- **Urgent and standard, never mixed.** Urgent requests have a tighter deadline and usually a different pattern. Blending them with standard requests would hide exactly what the rule wants to reveal, so they are reported separately throughout.

> In plain words: the numbers are easy to get roughly right and surprisingly easy to get subtly wrong, and a subtly wrong scorecard is worse than none, because people trust it.

## Why a public scorecard helps

It is worth pausing on why the rule bothers to make these numbers public at all.

When performance is hidden, a plan that is slow or that denies a great deal has little pressure to change, because no outsider can see it. When the same figures are posted every year on a public page, several things happen. Patients choosing a plan can compare. Employers choosing coverage for their staff can ask hard questions. Doctors can point to the numbers when a plan's behaviour does not match its promises. And regulators have a yearly, like-for-like record to watch over time.

None of this works unless the figures are produced the same way by everyone, which is why the rule points to a single template and a single set of definitions. A scorecard that each plan laid out differently would be almost impossible to compare, and comparison is the whole point.

## Common questions

**Does the plan have to show individual patients' requests?**
No. The page shows totals and percentages across all requests, not any one person's case.

**What counts as "approved" if a request was only partly approved?**
A trustworthy tool counts a partial approval as an approval, because the template has no separate category for it, and says plainly how many partial approvals there were, so the number is honest.

**What about requests that were still pending when the year ended?**
They are not counted as decided, because they were not. A good tool lists them as excluded rather than quietly dropping or miscounting them.

**Why does the unit matter so much?**
Because "3 days" and "3 hours" are very different, and a median faster than a day is easy to round to a meaningless "0 days". The rule asks for the unit to be written, and a good tool always writes it.

**Who maintains the page once it is posted?**
Whoever runs the plan's website. That is why the page is built to stand entirely on its own, with nothing it needs to fetch from elsewhere.

## How Perfuse does it

![A CSV of decisions turned into a public page, spreadsheet and data](docs/assets/articles/metrics-flow.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is free, open-source software that produces the metrics in the layout of **CMS's own template**. It is one file to download, licensed under Apache 2.0, so there is no fee.

- **It reads a plain spreadsheet** exported from any approval system. It finds the columns by their names, in any order: things like the request id, the line of business, whether it was urgent, when it arrived, when it was decided, the decision, whether extra time was taken, whether it was appealed and how, and the service.
- **It produces every figure the rule names**, with urgent and standard requests reported separately, for each line of business.
- **It counts approvals and denials** within the deadline, after an extension, and after appeal.
- **It reports the average and the middle decision time**, always with the unit written out.
- **It uses the right deadlines**: 72 hours for urgent and 7 days for standard by default, adjustable for plans that operate under different timeframes.
- **It reports data quality in plain words.** Rows it cannot read are listed with their line numbers. Requests still pending, or decided outside the year, are counted and set aside. A missing list of what needs approval is flagged, because the rule wants that list on the same page.
- **It gives three outputs:** a self-contained public web page ready to publish, a spreadsheet with one row per figure, and data for any other use.

The page stands entirely on its own, with no script or outside content, because it is posted on a public website and anything it loaded from elsewhere could break or quietly track a visitor.

The metrics sit alongside everything else the rule asks of a plan, in the same software: the four computer connections (Patient Access, Provider Access, Payer-to-Payer and Prior Authorization), claims in the CARIN shape, and the Da Vinci prior authorization recipes CRD, DTR and PAS.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest).
2. Export last year's decisions to a spreadsheet and run the metrics command.
3. Read the [manual](https://perfuse.health/manual/) for the column names it looks for.

Related reading: [CMS-0057-F explained](https://perfuse.health/cms-0057-explained/) · [Da Vinci CRD, DTR and PAS explained](https://perfuse.health/da-vinci-prior-authorization/) · [X12 278 and FHIR PAS compared](https://perfuse.health/x12-278-vs-pas/).

## Glossary

| Term | What it means |
|---|---|
| Appeal | A patient's formal request to the plan to reconsider a decision. |
| Average | The usual arithmetic average of a set of numbers. |
| CHIP | Children's Health Insurance Program, public coverage for children. |
| CMS | Centers for Medicare and Medicaid Services, the US government body behind the rule. |
| CMS-0057-F | The US rule that requires the yearly metrics. |
| CSV | A plain spreadsheet file, one row per record. |
| Extension | When a plan takes extra time beyond its normal deadline to decide. |
| Line of business | A category of plan, such as Medicare Advantage or Medicaid, reported separately. |
| Median (the middle) | The point where half the values are higher and half are lower. |
| Medicaid | Public health coverage for people with low income. |
| Medicare Advantage | Private plans that run Medicare benefits. |
| Metrics | The published figures about the plan's performance. |
| Payer | An organization that pays for healthcare; an insurance plan. |
| Prior authorization | Asking the health plan for permission before a test or treatment. |
| QHP | Qualified Health Plan, a plan sold on the government's HealthCare.gov marketplace. |

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
