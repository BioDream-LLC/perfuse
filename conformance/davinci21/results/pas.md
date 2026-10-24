# PAS: Perfuse's answers to the 2.1.0 guide's own example requests

## Client asks for 2.1: checked against PAS 2.1.0

| Answer | Profile | Errors in Perfuse's answer | In the guide's example data |
|---|---|---|---|
| submit-HomecareAuthorizationBundleExample | `profile-pas-response-bundle` | 0 | an example.org URL in the guide's example data; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| submit-MedicalServicesAuthorizationBundleExample | `profile-pas-response-bundle` | 0 | an example.org URL in the guide's example data; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| submit-ReferralAuthorizationBundleExample | `profile-pas-response-bundle` | 0 | an example.org URL in the guide's example data; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| submit-SurgicalRequestBundleExample | `profile-pas-response-bundle` | 0 | an example.org URL in the guide's example data; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| submit-HomecareAuthorizationUpdateBundleExample | `profile-pas-response-bundle` | 0 | an example.org URL in the guide's example data; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| inquire | `profile-pas-inquiry-response-bundle` | 0 | an example.org URL in the guide's example data; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |

## Client names no version: checked against PAS 2.2.1

| Answer | Profile | Errors in Perfuse's answer | In the guide's example data |
|---|---|---|---|
| submit-HomecareAuthorizationBundleExample | `profile-pas-response-bundle` | 0 | an example.org URL in the guide's example data; the 2.1.0 example Coverage has no identifier, which PAS 2.2.1 requires; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| submit-MedicalServicesAuthorizationBundleExample | `profile-pas-response-bundle` | 0 | an example.org URL in the guide's example data; the 2.1.0 example Coverage has no identifier, which PAS 2.2.1 requires; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| submit-ReferralAuthorizationBundleExample | `profile-pas-response-bundle` | 0 | an example.org URL in the guide's example data; the 2.1.0 example Coverage has no identifier, which PAS 2.2.1 requires; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| submit-SurgicalRequestBundleExample | `profile-pas-response-bundle` | 0 | an example.org URL in the guide's example data; follows from the LOINC attachment code the validator could not check; terminology is off, so the validator cannot check a LOINC attachment code against its value set; the 2.1.0 example Coverage has no identifier, which PAS 2.2.1 requires; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| submit-HomecareAuthorizationUpdateBundleExample | `profile-pas-response-bundle` | 0 | an example.org URL in the guide's example data; the 2.1.0 example Coverage has no identifier, which PAS 2.2.1 requires; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| inquire-1 | `profile-pas-inquiry-response-bundle` | 0 | an example.org URL in the guide's example data; the 2.1.0 example Coverage has no identifier, which PAS 2.2.1 requires; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| inquire-2 | `profile-pas-inquiry-response-bundle` | 0 | an example.org URL in the guide's example data; the 2.1.0 example Coverage has no identifier, which PAS 2.2.1 requires; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| inquire-3 | `profile-pas-inquiry-response-bundle` | 0 | an example.org URL in the guide's example data; the 2.1.0 example Coverage has no identifier, which PAS 2.2.1 requires; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| inquire-4 | `profile-pas-inquiry-response-bundle` | 0 | an example.org URL in the guide's example data; the 2.1.0 example Coverage has no identifier, which PAS 2.2.1 requires; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| inquire-5 | `profile-pas-inquiry-response-bundle` | 0 | an example.org URL in the guide's example data; the 2.1.0 example Coverage has no identifier, which PAS 2.2.1 requires; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |
| inquire-6 | `profile-pas-inquiry-response-bundle` | 0 | an example.org URL in the guide's example data; the 2.1.0 example Coverage has no identifier, which PAS 2.2.1 requires; the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too) |

