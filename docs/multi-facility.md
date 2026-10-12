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
| Roles | Monitor (status and counts, no message contents), viewer, operator (resend without changing), editor, admin; SSO (OIDC, SAML, LDAP) maps directory groups to viewer, editor and admin | Equivalent: monitoring is separate from seeing content, and resending from changing |
| Rights inside one organisation | Channel groups: a person or token limited to some groups sees and acts on those channels only, in every list, message view and search, count, chart, queue, alert and metric; a central team with no limit sees all | Equivalent to rights per Locker, with one difference: a person's groups apply to every role they hold rather than a different role per group |
| Administration per facility | An administrator limited to groups manages their groups' people and tokens, starts and stops their channels and clears their queues; server-wide settings stay with administrators who see every channel | Equivalent |
| Limits per organisation | Each tenant's channels, messages a minute and stored megabytes, set by the platform operator; over a limit work is refused (senders resend), never dropped | Not part of Lockers |
| Deployment between environments | Each tenant's channels are a directory of plain files, kept in Git, so changes are reviewed and promoted as commits | Equivalent, through Git rather than export files |

## What is still different

- **SSO and the new roles.** OIDC, SAML and LDAP map directory groups to viewer, editor and admin; there is no mapping yet to
  monitor or operator, or from a directory group to channel groups. Channel groups set on a federated person in Users are kept
  across sign-ins.
- **One role per person.** A Locker can give somebody change rights in one facility and view rights in another. In Perfuse a
  person has one role, applied to all of their groups; two accounts give the split.
- **Shared resources.** Code tables, script libraries and prior-authorisation decisions are shared by the whole tenant; a person
  limited to groups can read them but not change them, rather than having a shared locker of their own.
