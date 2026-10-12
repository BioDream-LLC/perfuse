# Multi-facility operation: Perfuse tenants and Rhapsody Lockers

Rhapsody's Lockers are how one Rhapsody engine serves several facilities or customers. This page compares them with Perfuse's
tenants and channel groups, and lists what Perfuse lacks. Rhapsody's own documentation needs a customer login, so the description
of Lockers below comes from Rhapsody's public material and integrators' write-ups, and may miss detail.

## What a Locker is

A Locker is a top-level partition of one Rhapsody engine's configuration. Each holds folders of routes and communication points.
Users and groups are given rights per Locker (to view, to change, to monitor, to see message content), and one person can hold
rights in several Lockers. Monitoring and message tracking honour those rights. Integrators also use Lockers as the unit of
deployment between environments (development, test, production). The typical use is a health system with many facilities, each
facility's interfaces in its own Locker, and a central integration team that sees all of them.

## What Perfuse has

| Need | Perfuse | Compared with Lockers |
|---|---|---|
| Several organisations in one engine | Tenants (`-multi-tenant`): channels, users, messages, queues, audit trail, FHIR resources and metrics labels belong to one tenant, and the store cannot be read without a tenant handle | Stronger isolation: a tenant cannot see another's data even through a mistake in a query |
| Administration per organisation | A tenant's admin manages its users and settings; platform accounts create tenants | Equivalent |
| Roles | Viewer, editor, admin per tenant; SSO (OIDC, SAML, LDAP) with group-to-role mapping | Coarser: one role for the whole tenant |
| Grouping inside one organisation | Channel `group`, for organising the list | Organising only; no rights attach to it |
| Deployment between environments | Each tenant's channels are a directory of plain files, kept in Git, so changes are reviewed and promoted as commits | Equivalent, through Git rather than export files |

## Gaps, queued

1. **One account with rights in several partitions.** A Perfuse user belongs to one tenant, and the manual advises against tenants
   for departments that must see each other's traffic, because there is no cross-tenant view. A health system wants exactly what
   Lockers give: each facility separate, and a central team that sees all. Proposed: access rights on channel groups within one
   tenant (a user or SSO group allowed groups A and B; empty means all), applied to every list, message view, queue and metric, with
   the tenant boundary kept for separate organisations.
2. **Finer rights than the three roles.** Lockers separate "may monitor" from "may see message content". Perfuse's viewer sees
   content. Proposed: a monitor role (status, counts and errors, no message bodies), and the right to change a channel separate from
   the right to resend messages.
3. **A dashboard per partition.** Perfuse's dashboard covers the tenant. With group rights, the dashboard, alerts and metrics should
   filter to the groups a user holds.

Not part of Lockers, but asked by service providers and also queued: limits per tenant (message rate, storage, number of channels),
so one customer cannot exhaust a shared engine.
