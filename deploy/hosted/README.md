# Hosted Perfuse in the EU and the UK

OpenTofu (or Terraform) for one hosted Perfuse per organisation, in one AWS Region, built so that the organisation's data
stays in that Region. A stack per Region:

| Stack | Region | City | Perfuse runs with |
|---|---|---|---|
| `regions/eu-central-1` | eu-central-1 | Frankfurt | `-residency eea` |
| `regions/eu-west-1` | eu-west-1 | Ireland | `-residency eea` |
| `regions/eu-west-2` | eu-west-2 | London | `-residency uk` |

The module (`modules/perfuse-region`) also accepts the other European Regions (Paris, Stockholm, Milan, Spain, Zurich).

## What it builds

- **A VPC** with two public subnets (the load balancer) and two private ones (the server and the database), one NAT gateway
  (`nat_gateway`), an S3 gateway endpoint, and interface endpoints for Session Manager, CloudWatch Logs and Secrets Manager
  (`private_endpoints`), so support access, logs and the database password never cross the internet.
- **The server:** Amazon Linux 2023 on Graviton (`instance_type`, t4g.medium by default) in a private subnet, its disk
  encrypted, IMDSv2 only. First boot (`setup.sh.tftpl`) installs the Perfuse release named, checked against the release's
  `SHA256SUMS` (from the Region's backup bucket under `releases/<version>/` when it is there, else from GitHub), and runs it
  under systemd, hardened, on PostgreSQL over checked TLS, with `-residency`. The database password is read from Secrets
  Manager and given to the driver as `PGPASSWORD` from a root-owned file, never on the command line.
- **PostgreSQL 16** on RDS (`db_instance_class`; a standby in a second zone with `db_multi_az`), encrypted, private,
  deletion-protected, automated backups for `db_backup_days` in the Region and no cross-Region copy.
- **A backup bucket** for Perfuse's own files (settings, channels, sign-in configuration), backed up nightly: encrypted with
  the Region's key, versioned, TLS only, public access blocked, expired after `backup_days`, and no replication rule.
- **CloudWatch Logs** for the server's log and for every support session (Session Manager records each one), encrypted,
  kept `log_days`.
- **One KMS key** in the Region (not multi-Region) for all of the above.
- **A Network Load Balancer:** TLS 1.2/1.3 on 443 (the console, FHIR) and on any `mllp_ports`, with the organisation's ACM
  certificate, and TLS again to the server, so no hop carries the traffic in clear. `allowed_cidrs` limits who may connect.

## What "the data stays in the Region" covers, and what it does not

Covered: the server, the database and its backups, the file backups, the logs, the support-session records, the keys, and
the Terraform state if it is kept in the Region (the stacks show the backend to use). Perfuse's `-residency` refuses any
channel whose S3, SQS or SNS connector, or whose destination's AWS hostname, is in another Region, and refuses to start with
a trace collector or alert webhook in one.

Not covered, because nothing here can decide it: where the organisation's own systems and partners are. Those are the hosts
in the record of processing (Data protection screen in Perfuse), for the controller to check. Data sent to a hospital's own
MLLP listener goes wherever that listener is.

Also outside this module: an AWS Organizations service control policy denying other Regions to the account, which is the
belt to this module's braces and is set by whoever runs the AWS organisation.

## Using it

```sh
cd regions/eu-west-2
tofu init
tofu plan -var customer=example -var domain=perfuse.example.org \
  -var certificate_arn=arn:aws:acm:eu-west-2:ACCOUNT:certificate/ID -var perfuse_version=v0.1.17
tofu apply ...
```

Then point the domain at the `address` output, and copy `where_the_data_is` into Perfuse's **Settings → Data → Where the data
is kept**. Support: `aws ssm start-session --region eu-west-2 --target <server output>`.

## Verified so far

- `tofu validate` passes for all three stacks (OpenTofu 1.13.1, AWS provider 6.69.1, pinned in `.terraform.lock.hcl`).
- `tofu plan` for eu-west-2 against a real AWS account: 55 resources to add, and every Region named in the plan is
  eu-west-2.
- **Not yet applied.** Nothing has been built from this, so the first boot script, the TLS hop to the server and the Session
  Manager preferences have not run on a real server.

## What it costs

List prices from the AWS Price List API on 18 October 2026, in USD, on demand, before tax, for 730 hours: the default
sizes, 50 GB of database storage, 30 GB of disk, 20 GB of backups, 5 GB of logs a month, one load balancer capacity unit
and 20 GB through the NAT gateway. Not a quote. Data sent out to the internet is extra beyond AWS's free allowance; reserved
instances or savings plans lower the server and database lines by roughly a third to a half.

| Item | Frankfurt | Ireland | London |
|---|---:|---:|---:|
| Server (t4g.medium) | 28.03 | 26.86 | 27.45 |
| PostgreSQL (db.t4g.medium, one zone) | 54.02 | 50.37 | 52.56 |
| Database storage (50 GB gp3) | 6.85 | 6.35 | 6.65 |
| Server disk (30 GB gp3) | 2.86 | 2.64 | 2.78 |
| Load balancer (and one LCU) | 24.09 | 22.78 | 23.70 |
| NAT gateway (and 20 GB) | 39.00 | 36.00 | 37.50 |
| Five VPC endpoints, one zone | 43.80 | 40.15 | 40.15 |
| Backups (20 GB S3) | 0.49 | 0.46 | 0.48 |
| Logs (5 GB in, 5 GB kept) | 3.31 | 3.00 | 3.15 |
| Secret, and the key (1.00 a month, AWS's published KMS price) | 1.40 | 1.40 | 1.40 |
| **A month** | **203.85** | **190.01** | **195.82** |
| A standby database in a second zone (`db_multi_az`) | +61.60 | +56.72 | +59.94 |

Ireland is the cheapest of the three, London close, Frankfurt the dearest by about 7%. Without the VPC endpoints
(`private_endpoints = false`, the traffic going to the same Regional services through the NAT gateway) a month is about
40 less.
