# TEFCA and UDAP explained: national health data exchange, in plain terms

![A network of organisations connected through a QHIN hub](docs/assets/articles/hero-tefca.svg)

By **BioDream Developer**, BioDream LLC

For years, sharing records between two US healthcare organisations meant setting up a separate connection, contract and trust arrangement with each one. **TEFCA**, the **Trusted Exchange Framework and Common Agreement**, changes that: join once, and you can exchange records with organisations across the country.

This guide explains how TEFCA works, where FHIR and UDAP fit in, and how to take part.

## The framework

TEFCA was set up under the 21st Century Cures Act and is overseen by **ASTP/ONC**, with **The Sequoia Project** serving as its Recognized Coordinating Entity. It has three layers:

- **QHINs**, Qualified Health Information Networks, are the national networks that connect to each other.
- **Participants** are organisations that join a QHIN: health systems, health information exchanges, payers, technology vendors, public health agencies.
- **Subparticipants** join through a Participant, such as a clinic connecting through its health system.

Everyone signs on to the same **Common Agreement**, so trust is established once rather than pair by pair.

## Why organisations exchange

TEFCA defines the **exchange purposes** for which records may be requested:

- Treatment
- Payment
- Health care operations
- Public health
- Government benefits determination
- Individual access services, where a patient requests their own records

## FHIR and Facilitated FHIR

The Common Agreement brought **FHIR** into TEFCA alongside document-based exchange. With **Facilitated FHIR exchange**, the QHINs help participants find each other and establish trust, and the FHIR requests and responses then flow between the participants directly.

## UDAP: trust between strangers

FHIR APIs normally require a client to be registered with each server ahead of time. Across a national network, that does not scale. **UDAP**, profiled for FHIR in HL7's **FAST Security** guide, solves it with certificates:

- **Dynamic client registration**: a client registers itself, proving who it is with a certificate from a trusted community.
- **Signed metadata**: each server publishes signed details of its endpoints, so a client can confirm it is talking to the real server.
- **Trust communities**: certificates chain to a community's authority, so every member trusts every other.

## How Perfuse does it

![Your organisation and another exchanging records across two QHINs, with UDAP and auditing](docs/assets/articles/tefca-udap.svg)

**[Perfuse](https://github.com/biodream-llc/perfuse)** is a free, Apache-2.0 healthcare integration engine with TEFCA exchange built in.

- **UDAP**: dynamic client registration, discovery, and **signed metadata verification**, checked against a real authorization server and a real trust community.
- **Facilitated FHIR exchange** between TEFCA participants, following the SOP.
- **Partner discovery** with certificate chain verification.
- **Every exchange audited**, and the audit is written to disk, not held in memory.
- **The data to exchange**: Perfuse converts HL7 v2 feeds to FHIR and US Core 9.0.0, serves them from its built-in FHIR endpoint, and validates them with the official HL7 validator.
- **Enterprise security** around it: SAML, OpenID Connect, LDAP, passkeys, SCIM, mutual TLS, and a built-in SMART on FHIR authorization server.

All of this is in the same single binary as the rest of the engine, with the web console built in, so the system that feeds your records is also the one that exchanges them.

## Get started

1. Download Perfuse from the [latest release](https://github.com/biodream-llc/perfuse/releases/latest).
2. Run `perfuse serve` and open http://127.0.0.1:8080.
3. Read the [manual](https://perfuse.health/manual/) for TEFCA and UDAP setup.

Related: [SMART on FHIR explained](https://perfuse.health/smart-on-fhir/) · [CMS-0057-F explained](https://perfuse.health/cms-0057-explained/).
