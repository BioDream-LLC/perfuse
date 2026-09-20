// The browser side of WebAuthn.
//
// The awkward part is that the API speaks base64url and the browser API speaks ArrayBuffer, in both directions and at several
// levels of nesting. Getting one conversion wrong produces a credential that registers and then never verifies, and the error
// surfaces at the next sign-in rather than at the mistake - so the conversions live here, once, rather than inline.

/** The registration options as the server sends them: every binary field base64url.
 *
 *  Named PasskeyCreationOptions rather than the specification's own name, because the DOM now
 *  declares a type with that name and the two differ in which fields are optional. Two types with
 *  one name is a confusing afternoon. */
export interface PasskeyCreationOptions {
  challenge: string
  rp: { id: string; name: string }
  user: { id: string; name: string; displayName: string }
  pubKeyCredParams: { type: string; alg: number }[]
  timeout: number
  authenticatorSelection?: {
    residentKey?: string
    requireResidentKey?: boolean
    userVerification?: string
  }
  excludeCredentials?: { type: string; id: string; transports?: string[] }[]
  attestation?: string
}

/** The sign-in options as the server sends them. */
export interface PasskeyRequestOptions {
  challenge: string
  rpId: string
  timeout: number
  allowCredentials?: { type: string; id: string; transports?: string[] }[]
  userVerification?: string
}

/** Whether this browser can do passkeys at all.
 *
 *  Checked before showing anything, because a button that does nothing is worse than no button:
 *  somebody clicks it, nothing happens, and they conclude the product is broken rather than
 *  that their browser is old. */
export function passkeysSupported(): boolean {
  return (
    typeof window !== 'undefined' &&
    typeof window.PublicKeyCredential !== 'undefined' &&
    typeof navigator.credentials?.create === 'function'
  )
}

/** Whether this device can offer a passkey with user verification.
 *
 *  Distinct from support: a browser may implement WebAuthn while the machine has no biometric or
 *  PIN. Used to decide whether to offer passkeys as a sign-in method at all, since the server
 *  requires verification. */
export async function platformAuthenticatorAvailable(): Promise<boolean> {
  if (!passkeysSupported()) return false
  const available = window.PublicKeyCredential?.isUserVerifyingPlatformAuthenticatorAvailable
  if (typeof available !== 'function') return false
  try {
    return await available.call(window.PublicKeyCredential)
  } catch {
    // A browser that throws here is one whose answer we do not have. Treated as unavailable,
    // because offering a method that fails is worse than not offering it.
    return false
  }
}

/** base64url to bytes. */
function fromBase64Url(value: string): Uint8Array {
  // The browser produces base64url without padding and atob wants base64 with it, so both
  // differences are undone here.
  const base64 = value.replace(/-/g, '+').replace(/_/g, '/')
  const padded = base64 + '='.repeat((4 - (base64.length % 4)) % 4)
  const binary = atob(padded)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
  return bytes
}

/** bytes to base64url. */
function toBase64Url(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i] ?? 0)
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** Creates a passkey, returning what the server needs to verify it. */
export async function createPasskey(
  options: PasskeyCreationOptions,
): Promise<{ challenge: string; response: unknown }> {
  const credential = (await navigator.credentials.create({
    publicKey: {
      challenge: fromBase64Url(options.challenge),
      rp: options.rp,
      user: {
        id: fromBase64Url(options.user.id),
        name: options.user.name,
        displayName: options.user.displayName,
      },
      pubKeyCredParams: options.pubKeyCredParams as PublicKeyCredentialParameters[],
      timeout: options.timeout,
      authenticatorSelection:
        options.authenticatorSelection as AuthenticatorSelectionCriteria | undefined,
      excludeCredentials: (options.excludeCredentials ?? []).map((c) => ({
        type: 'public-key' as const,
        id: fromBase64Url(c.id),
        transports: c.transports as AuthenticatorTransport[] | undefined,
      })),
      attestation: options.attestation as AttestationConveyancePreference | undefined,
    },
  })) as PublicKeyCredential | null

  if (!credential) {
    throw new Error('the browser returned no credential')
  }

  const response = credential.response as AuthenticatorAttestationResponse

  return {
    // The challenge is echoed back rather than kept only on the server, because the server has to
    // look it up to find which account started this - and it deletes it as it does, which is what
    // makes it single-use.
    challenge: options.challenge,
    response: {
      id: credential.id,
      rawId: toBase64Url(credential.rawId),
      type: credential.type,
      response: {
        clientDataJSON: toBase64Url(response.clientDataJSON),
        attestationObject: toBase64Url(response.attestationObject),
      },
    },
  }
}

/** Signs in with a passkey. */
export async function usePasskey(
  options: PasskeyRequestOptions,
): Promise<{ challenge: string; response: unknown }> {
  const credential = (await navigator.credentials.get({
    publicKey: {
      challenge: fromBase64Url(options.challenge),
      rpId: options.rpId,
      timeout: options.timeout,
      allowCredentials: (options.allowCredentials ?? []).map((c) => ({
        type: 'public-key' as const,
        id: fromBase64Url(c.id),
        transports: c.transports as AuthenticatorTransport[] | undefined,
      })),
      userVerification: options.userVerification as UserVerificationRequirement | undefined,
    },
  })) as PublicKeyCredential | null

  if (!credential) {
    throw new Error('the browser returned no credential')
  }

  const response = credential.response as AuthenticatorAssertionResponse

  return {
    challenge: options.challenge,
    response: {
      id: credential.id,
      rawId: toBase64Url(credential.rawId),
      type: credential.type,
      response: {
        clientDataJSON: toBase64Url(response.clientDataJSON),
        authenticatorData: toBase64Url(response.authenticatorData),
        signature: toBase64Url(response.signature),
        userHandle: response.userHandle ? toBase64Url(response.userHandle) : '',
      },
    },
  }
}

/** Turns a WebAuthn failure into something a person can act on.
 *
 *  The browser's own messages are unhelpful by design - they deliberately avoid saying why an
 *  authenticator refused - so the name of the error is more use than its message. */
export function explainPasskeyError(err: unknown): string {
  if (err instanceof Error) {
    switch (err.name) {
      case 'NotAllowedError':
        // Covers both a cancellation and a timeout, and the browser will not say which.
        return 'The request was cancelled or timed out. Try again and confirm on your device.'
      case 'InvalidStateError':
        return 'This device already has a passkey for your account.'
      case 'NotSupportedError':
        return 'This device cannot create the kind of passkey this server asks for.'
      case 'SecurityError':
        return 'The page address does not match what passkeys are configured for on this server.'
      case 'AbortError':
        return 'The request was stopped before it finished.'
      default:
        return err.message || 'The passkey request failed.'
    }
  }
  return 'The passkey request failed.'
}
