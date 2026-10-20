import { Advanced, Area, Check, Choose, Num, Pair, Secret, Text } from './builderFields'
import {
  FTPSourceFields,
  LocalFileFields,
  SMBSourceFields,
  SerialSourceFields,
  TCPSourceFields,
  WebDAVSourceFields,
} from './BuilderFileSources'
import type { ChannelDraft, SourceKind } from './model'
import { pgpSourceKinds } from './model'
import { useState } from 'react'

// Where messages arrive, for every transport the engine supports.
//
// Until now this form could only build an MLLP source, so anybody needing to accept messages
// over HTTP, collect them from an SFTP server or read them out of a database had to write the
// file by hand - which is exactly the situation the builder exists to remove.
//
// The hints carry the things that actually go wrong, because that is what somebody is paying a
// consultant for. A listener bound to 127.0.0.1 will never see a hospital feed. An HTTP
// endpoint with no token accepts a message from anyone who can reach the port. An SFTP
// collector that does not move files reads them again on the next poll and delivers every
// message twice. None of that is discoverable from a field label.

const SOURCE_OPTIONS: { value: SourceKind; label: string }[] = [
  { value: 'mllp', label: 'A hospital system connects to us (MLLP)' },
  { value: 'http', label: 'A sender posts messages to us (HTTP)' },
  { value: 'sftp', label: 'We collect files from a server (SFTP)' },
  { value: 'database', label: 'We poll a database table' },
  { value: 'soap', label: 'A sender calls us as a web service (SOAP)' },
  { value: 'dicom', label: 'A scanner sends us images (DICOM C-STORE)' },
  { value: 'dicom_query', label: 'We ask an imaging archive what is new (DICOM C-FIND)' },
  { value: 'dicomweb', label: 'A viewer or service sends us images over the web (DICOMweb STOW-RS)' },
  { value: 'direct', label: 'Our HISP delivers Direct secure messages to us (Direct, SMTP)' },
  { value: 'as2', label: 'Trading partners send us EDI over AS2 (signed, encrypted, with a receipt)' },
  { value: 'javascript', label: 'A script we write produces messages (JavaScript Reader)' },
  { value: 'file', label: 'We read files from a folder on this server' },
  { value: 'ftp', label: 'We collect files from a server (FTP or FTPS)' },
  { value: 'smb', label: 'We collect files from a Windows share' },
  { value: 'webdav', label: 'We collect files from a WebDAV collection' },
  { value: 'tcp', label: 'A device connects to us on a socket, not MLLP (TCP)' },
  { value: 'serial', label: 'A device is wired to us (serial cable)' },
  { value: 'broker', label: 'We read from a message queue (ActiveMQ, RabbitMQ)' },
  { value: 'kafka', label: 'We read from a Kafka topic' },
  { value: 'sqs', label: 'We read from an Amazon SQS queue' },
  { value: 's3', label: 'We collect objects from an Amazon S3 bucket' },
  { value: 'amqp', label: 'We read from an AMQP 1.0 queue (Azure Service Bus, RabbitMQ 4)' },
  { value: 'azure_blob', label: 'We collect blobs from Azure Blob Storage' },
  { value: 'email', label: 'We read a mailbox (IMAP or POP3: Microsoft 365, Gmail, Exchange)' },
  { value: 'mqtt', label: 'We subscribe to an MQTT topic (Mosquitto, AWS IoT Core, HiveMQ)' },
  { value: 'ibm_mq', label: 'We read from an IBM MQ queue' },
  { value: 'pubsub', label: 'We read from a Google Cloud Pub/Sub subscription' },
  { value: 'gcs', label: 'We collect objects from Google Cloud Storage' },
]

export function SourceSection({
  draft,
  set,
}: {
  draft: ChannelDraft
  set: <K extends keyof ChannelDraft>(key: K, value: ChannelDraft[K]) => void
}) {
  const [showLimits, setShowLimits] = useState(false)
  const [showSoapAdvanced, setShowSoapAdvanced] = useState(false)
  const [showHttpAdvanced, setShowHttpAdvanced] = useState(false)
  const [showKafkaSasl, setShowKafkaSasl] = useState(false)
  const [showDirectAdvanced, setShowDirectAdvanced] = useState(false)
  const [showDirectImap, setShowDirectImap] = useState(false)
  const [showDirectXdr, setShowDirectXdr] = useState(false)
  const [showDicomAdvanced, setShowDicomAdvanced] = useState(false)
  const [showMllpTls, setShowMllpTls] = useState(false)
  const [showPgp, setShowPgp] = useState(draft.srcPgpEnabled)
  const isX12 = draft.dataType === 'x12'

  return (
    <div className="space-y-4">
      <Choose
        label="How do messages get here?"
        value={draft.sourceKind}
        onChange={(sourceKind) => set('sourceKind', sourceKind)}
        options={SOURCE_OPTIONS}
        hint={
          isX12
            ? 'MLLP is an HL7 transport and is refused on an X12 channel. Clearing houses send X12 over HTTP or SFTP.'
            : undefined
        }
      />

      {draft.sourceKind === 'mllp' && (
        <Pair>
          <Text
            label="Listen on"
            required
            value={draft.listen}
            onChange={(v) => set('listen', v)}
            placeholder=":6661"
            mono
            hint="A port on its own accepts connections from anywhere. Adding an address restricts it — and 127.0.0.1 restricts it to this machine, which is the most common reason a feed never connects."
          />
          <Text
            label="Identify ourselves as"
            value={draft.ackApplication}
            onChange={(v) => set('ackApplication', v)}
            placeholder="PERFUSE"
            hint="Goes in MSH-3 of the acknowledgement. Leave empty to mirror whatever the sender addressed."
          />
        </Pair>
      )}

      {draft.sourceKind === 'mllp' && (
        <Advanced
          title="Encrypt this listener"
          hint="An MLLP listener is its own server with its own certificate, separate from the TLS on Perfuse's web interface."
          open={showMllpTls}
          onToggle={() => setShowMllpTls((v) => !v)}
        >
          <Check
            label="Encrypt connections to this listener with TLS"
            value={draft.mllpTlsEnabled}
            onChange={(v) => set('mllpTlsEnabled', v)}
            hint="Many older interface engines cannot do this, which is why it is a choice rather than enforced. HL7 over plain MLLP carries patient data in clear text across whatever network sits between."
          />

          {draft.mllpTlsEnabled && (
            <>
              <Pair>
                <Text
                  label="Certificate file"
                  value={draft.mllpTlsCertFile}
                  onChange={(v) => set('mllpTlsCertFile', v)}
                  placeholder="/etc/perfuse/mllp.pem"
                  mono
                />
                <Text
                  label="Private key file"
                  value={draft.mllpTlsKeyFile}
                  onChange={(v) => set('mllpTlsKeyFile', v)}
                  placeholder="/etc/perfuse/mllp.key"
                  mono
                />
              </Pair>

              <Check
                label="Require a client certificate"
                value={draft.mllpTlsRequireClientCert}
                onChange={(v) => set('mllpTlsRequireClientCert', v)}
                hint="Mutual TLS. On an MLLP listener this is the only authentication available - there is no token and no header - so without it any host that can reach the port can send messages into this channel."
              />

              {draft.mllpTlsRequireClientCert && (
                <Text
                  label="Certificate authority to trust"
                  value={draft.mllpTlsCAFile}
                  onChange={(v) => set('mllpTlsCAFile', v)}
                  placeholder="/etc/perfuse/senders-ca.pem"
                  mono
                  hint="Required when a client certificate is demanded. Without it there is nothing to check the sender's certificate against."
                />
              )}
            </>
          )}
        </Advanced>
      )}

      {pgpSourceKinds.has(draft.sourceKind) && (
        <Advanced
          title="Open PGP-encrypted messages"
          hint="For payers, clearinghouses and agencies that send files under OpenPGP. Decrypted before anything else reads them."
          open={showPgp}
          onToggle={() => setShowPgp((v) => !v)}
        >
          <Check
            label="Messages arrive encrypted with OpenPGP"
            value={draft.srcPgpEnabled}
            onChange={(v) => set('srcPgpEnabled', v)}
            hint="Each file, object or body is decrypted whole, then split into messages as usual. One that will not open is refused, and a file source moves it to its error folder."
          />
          {draft.srcPgpEnabled && (
            <>
              <Pair>
                <Text
                  label="Our secret key"
                  value={draft.srcPgpDecryptWith}
                  onChange={(v) => set('srcPgpDecryptWith', v)}
                  placeholder="keys/our-secret.asc"
                  mono
                  hint="Armored or binary, as gpg --export-secret-keys writes it. Relative to the channel file. Give the sender the matching public key."
                />
                <Secret
                  label="Passphrase"
                  value={draft.srcPgpPassphrase}
                  onChange={(v) => set('srcPgpPassphrase', v)}
                  hint="Write ${PGP_PASSPHRASE} to read it from the environment. Checked when the channel starts, so a wrong one stops it there."
                />
              </Pair>
              <Text
                label="Senders' public keys"
                value={draft.srcPgpVerifyWith}
                onChange={(v) => set('srcPgpVerifyWith', v)}
                placeholder="keys/payer.asc, keys/payer-2027.asc"
                mono
                hint="Signatures by these keys are checked. Several are allowed, for a sender rolling over to a new key."
              />
              <Check
                label="Refuse a message these keys did not sign"
                value={draft.srcPgpRequireSignature}
                onChange={(v) => set('srcPgpRequireSignature', v)}
                hint="Encryption says only we can read it; a signature says who wrote it. Without this, anybody holding our public key can send to this channel."
              />
              <Check
                label="Accept messages that are signed but not encrypted"
                value={draft.srcPgpAllowUnencrypted}
                onChange={(v) => set('srcPgpAllowUnencrypted', v)}
              />
              <Check
                label="Let messages that are not PGP at all through unchanged"
                value={draft.srcPgpPassPlain}
                onChange={(v) => set('srcPgpPassPlain', v)}
                hint="For a folder that receives both. Off, a sender who forgot to encrypt is refused and told, which is usually what a contract requires."
              />
              {draft.srcPgpRequireSignature && !draft.srcPgpVerifyWith.trim() && (
                <p className="rounded border border-rose-700 bg-rose-950/40 px-2 py-1.5 text-xs text-rose-200">
                  Requiring a signature needs the senders' public keys. The server will refuse this channel otherwise.
                </p>
              )}
              {!draft.srcPgpDecryptWith.trim() && !draft.srcPgpAllowUnencrypted && (
                <p className="rounded border border-rose-700 bg-rose-950/40 px-2 py-1.5 text-xs text-rose-200">
                  Our secret key is needed to decrypt. Without one, only messages that are signed and not encrypted can be read.
                </p>
              )}
            </>
          )}
        </Advanced>
      )}

      {draft.sourceKind === 'http' && (
        <>
          <Pair>
            <Text
              label="Listen on"
              required
              value={draft.httpListen}
              onChange={(v) => set('httpListen', v)}
              placeholder="0.0.0.0:8080"
              mono
            />
            <Text
              label="Path"
              required
              value={draft.httpPath}
              onChange={(v) => set('httpPath', v)}
              placeholder="/messages"
              mono
            />
          </Pair>
          <Secret
            label="Token the sender must present"
            value={draft.httpToken}
            onChange={(v) => set('httpToken', v)}
            hint="Strongly recommended. Without one, anything that can reach this port can inject a message, and the message store will show it as having arrived legitimately."
          />

          <Advanced
            title="TLS and limits"
            hint="How this listener is encrypted and what it refuses. All of this was settable in the file and nowhere in the interface."
            open={showHttpAdvanced}
            onToggle={() => setShowHttpAdvanced((v) => !v)}
          >
            <Check
              label="Encrypt this listener with TLS"
              value={draft.httpTlsEnabled}
              onChange={(v) => set('httpTlsEnabled', v)}
              hint="Separate from the TLS on Perfuse's own web interface. A channel listening on its own port is its own server and needs its own certificate."
            />

            {draft.httpTlsEnabled && (
              <>
                <Pair>
                  <Text
                    label="Certificate file"
                    value={draft.httpTlsCertFile}
                    onChange={(v) => set('httpTlsCertFile', v)}
                    placeholder="/etc/perfuse/channel.pem"
                    mono
                  />
                  <Text
                    label="Private key file"
                    value={draft.httpTlsKeyFile}
                    onChange={(v) => set('httpTlsKeyFile', v)}
                    placeholder="/etc/perfuse/channel.key"
                    mono
                  />
                </Pair>

                <Check
                  label="Require a client certificate"
                  value={draft.httpTlsRequireClientCert}
                  onChange={(v) => set('httpTlsRequireClientCert', v)}
                  hint="Mutual TLS: the sender must present a certificate signed by the authority below. This is authentication, so a listener with it on does not also need a token — though having both is not wrong."
                />

                {draft.httpTlsRequireClientCert && (
                  <Text
                    label="Certificate authority to trust"
                    value={draft.httpTlsCAFile}
                    onChange={(v) => set('httpTlsCAFile', v)}
                    placeholder="/etc/perfuse/senders-ca.pem"
                    mono
                    hint="Required when a client certificate is demanded. Without it there is nothing to check the sender's certificate against."
                  />
                )}
              </>
            )}

            <Pair>
              <Text
                label="Give up reading after"
                value={draft.httpReadTimeout}
                onChange={(v) => set('httpReadTimeout', v)}
                placeholder="30s"
                mono
                hint="A sender that opens a connection and stops writing holds it open. Written as a duration: 30s, 2m."
              />
              <Text
                label="Largest message accepted (bytes)"
                value={draft.httpMaxMessageSize}
                onChange={(v) => set('httpMaxMessageSize', v)}
                placeholder="0"
                mono
                hint="Zero or empty means no limit, and the limit is then memory. A sender that posts a gigabyte is refused rather than absorbed."
              />
            </Pair>
          </Advanced>
        </>
      )}

      {(draft.sourceKind === 'sqs' || draft.sourceKind === 's3') && <AwsSourceAccess draft={draft} set={set} />}

      {draft.sourceKind === 'sqs' && (
        <>
          <Text
            label="Queue URL"
            required
            value={draft.sqsSrcQueueUrl}
            onChange={(v) => set('sqsSrcQueueUrl', v)}
            placeholder="https://sqs.eu-west-2.amazonaws.com/123456789012/adt"
            mono
            hint="Each message is deleted from the queue only after this channel has handled it. Until then it stays hidden, and comes back if Perfuse stops - so nothing is lost, and a redrive policy on the queue moves one that keeps failing to a dead-letter queue."
          />
          <Pair>
            <Text
              label="Long poll, seconds"
              value={draft.sqsSrcWaitSeconds}
              onChange={(v) => set('sqsSrcWaitSeconds', v)}
              placeholder="20"
              hint="1 to 20. An empty queue then costs one request every 20 seconds rather than a busy loop."
            />
            <Text
              label="Messages per request"
              value={draft.sqsSrcMaxMessages}
              onChange={(v) => set('sqsSrcMaxMessages', v)}
              placeholder="10"
              hint="1 to 10. They are handled one at a time, in order."
            />
          </Pair>
          <Text
            label="Visibility timeout"
            value={draft.sqsSrcVisibility}
            onChange={(v) => set('sqsSrcVisibility', v)}
            placeholder="60s"
            mono
            hint="How long a received message stays hidden while it is handled. Empty uses the queue's own setting. Make it longer than the slowest delivery, or a message comes back while still being delivered."
          />
        </>
      )}

      {draft.sourceKind === 's3' && (
        <>
          <Pair>
            <Text
              label="Bucket"
              required
              value={draft.s3SrcBucket}
              onChange={(v) => set('s3SrcBucket', v)}
              placeholder="hospital-inbound"
              mono
            />
            <Text
              label="Prefix to read"
              required
              value={draft.s3SrcPrefix}
              onChange={(v) => set('s3SrcPrefix', v)}
              placeholder="inbound/"
              mono
              hint="Ends in /. Handled objects move out of it, so they are not read again."
            />
          </Pair>
          <Pair>
            <Text
              label="Only keys ending in"
              value={draft.s3SrcSuffix}
              onChange={(v) => set('s3SrcSuffix', v)}
              placeholder=".hl7"
              mono
            />
            <Num
              label="Poll every, seconds"
              value={draft.s3SrcPollSeconds}
              onChange={(v) => set('s3SrcPollSeconds', v ?? 30)}
              placeholder="30"
            />
          </Pair>
          <Pair>
            <Choose
              label="After an object is handled"
              value={draft.s3SrcAfterRead}
              onChange={(v) => set('s3SrcAfterRead', v)}
              options={[
                { value: 'move', label: 'Move it to another prefix' },
                { value: 'delete', label: 'Delete it' },
              ]}
              hint="There is no leave-it-there: a bucket gives a poller nothing to remember across a restart, so every object would be read again."
            />
            {draft.s3SrcAfterRead === 'move' ? (
              <Text
                label="Move to"
                value={draft.s3SrcMoveTo}
                onChange={(v) => set('s3SrcMoveTo', v)}
                placeholder="processed/"
                mono
              />
            ) : (
              <span />
            )}
          </Pair>
          <Pair>
            <Text
              label="Objects that fail go to"
              value={draft.s3SrcErrorPrefix}
              onChange={(v) => set('s3SrcErrorPrefix', v)}
              placeholder="error/"
              mono
              hint="So one bad file does not stop the ones behind it."
            />
            <Text
              label="Largest object, bytes"
              value={draft.s3SrcMaxObjectSize}
              onChange={(v) => set('s3SrcMaxObjectSize', v)}
              placeholder="16777216"
            />
          </Pair>
          <Check
            label="Objects hold several MLLP-framed messages"
            value={draft.s3SrcFramed}
            onChange={(v) => set('s3SrcFramed', v)}
            hint="Otherwise each object is one message. Every message in an object must be accepted before the object is moved."
          />
          <Check
            label="Path-style addressing"
            value={draft.s3SrcPathStyle}
            onChange={(v) => set('s3SrcPathStyle', v)}
            hint="Needed by MinIO, Ceph and most S3-compatible stores."
          />
        </>
      )}

      {draft.sourceKind === 'amqp' && (
        <>
          <Pair>
            <Text
              label="Broker"
              required
              value={draft.amqpAddr}
              onChange={(v) => set('amqpAddr', v)}
              placeholder="contoso.servicebus.windows.net:5671"
              mono
              hint="host:port. 5671 with TLS for Service Bus and Event Hubs; 5672 plain for a broker on a trusted network."
            />
            <Text
              label="Queue or subscription"
              required
              value={draft.amqpAddress}
              onChange={(v) => set('amqpAddress', v)}
              placeholder="adt-inbound"
              mono
              hint="Service Bus: the queue name, or topic/subscriptions/name. RabbitMQ 4: /queues/name."
            />
          </Pair>
          <Pair>
            <Text
              label="Username"
              value={draft.amqpUsername}
              onChange={(v) => set('amqpUsername', v)}
              placeholder="RootManageSharedAccessKey"
              mono
              hint="For Service Bus, the shared access policy name. Empty for an anonymous broker."
            />
            <Secret
              label="Password"
              value={draft.amqpPassword}
              onChange={(v) => set('amqpPassword', v)}
              hint="For Service Bus, the policy's key. Write ${SERVICEBUS_KEY} to read it from the environment."
            />
          </Pair>
          <Check
            label="Encrypt the connection with TLS"
            value={draft.amqpTls}
            onChange={(v) => set('amqpTls', v)}
            hint="Required by Service Bus and Event Hubs. A message is accepted only after this channel has handled it; one that fails is released, and the broker redelivers it and dead-letters it past the queue's limit."
          />
          <Text
            label="Prefetch"
            value={draft.amqpPrefetch}
            onChange={(v) => set('amqpPrefetch', v)}
            placeholder="10"
            hint="How many the broker may send ahead. They are still handled one at a time, in order."
          />
        </>
      )}

      {draft.sourceKind === 'azure_blob' && (
        <>
          <Pair>
            <Text label="Storage account" required value={draft.azSrcAccount} onChange={(v) => set('azSrcAccount', v)} mono />
            <Text label="Container" required value={draft.azSrcContainer} onChange={(v) => set('azSrcContainer', v)} mono />
          </Pair>
          <Pair>
            <Secret
              label="SAS token"
              value={draft.azSrcSas}
              onChange={(v) => set('azSrcSas', v)}
              hint="Preferred: limit it to this container, with read, list, write and delete. Or ${AZURE_STORAGE_SAS}."
            />
            <Secret
              label="Account key"
              value={draft.azSrcKey}
              onChange={(v) => set('azSrcKey', v)}
              hint="Only if there is no SAS token. It unlocks the whole account."
            />
          </Pair>
          <Pair>
            <Text
              label="Prefix to read"
              required
              value={draft.azSrcPrefix}
              onChange={(v) => set('azSrcPrefix', v)}
              placeholder="inbound/"
              mono
            />
            <Text label="Only names ending in" value={draft.azSrcSuffix} onChange={(v) => set('azSrcSuffix', v)} placeholder=".hl7" mono />
          </Pair>
          <Pair>
            <Choose
              label="After a blob is handled"
              value={draft.azSrcAfterRead}
              onChange={(v) => set('azSrcAfterRead', v)}
              options={[
                { value: 'move', label: 'Move it to another prefix' },
                { value: 'delete', label: 'Delete it' },
              ]}
            />
            <Num
              label="Poll every, seconds"
              value={draft.azSrcPollSeconds}
              onChange={(v) => set('azSrcPollSeconds', v ?? 30)}
              placeholder="30"
            />
          </Pair>
          <Text
            label="Endpoint"
            value={draft.azSrcEndpoint}
            onChange={(v) => set('azSrcEndpoint', v)}
            placeholder="Leave empty for Azure"
            mono
            hint="Only for Azurite or a private endpoint, e.g. http://127.0.0.1:10000/devstoreaccount1."
          />
        </>
      )}

      {draft.sourceKind === 'email' && (
        <>
          <Pair>
            <Choose
              label="Protocol"
              value={draft.mailProtocol}
              onChange={(v) => {
                set('mailProtocol', v)
                if (v === 'pop3') set('mailAfterRead', 'delete')
                else if (draft.mailAfterRead === 'delete') set('mailAfterRead', 'move')
              }}
              options={[
                { value: 'imap', label: 'IMAP (folders: move or flag after reading)' },
                { value: 'pop3', label: 'POP3 (read and delete)' },
              ]}
            />
            <Choose
              label="Connection"
              value={draft.mailSecurity}
              onChange={(v) => set('mailSecurity', v)}
              options={[
                { value: 'tls', label: 'TLS (993 or 995)' },
                { value: 'starttls', label: 'STARTTLS (143 or 110)' },
                { value: 'none', label: 'None: only to this machine' },
              ]}
            />
          </Pair>
          <Pair>
            <Text
              label="Mail server"
              required
              value={draft.mailHost}
              onChange={(v) => set('mailHost', v)}
              placeholder="outlook.office365.com"
              mono
              hint="Microsoft 365: outlook.office365.com. Gmail: imap.gmail.com or pop.gmail.com."
            />
            <Text
              label="Port"
              value={draft.mailPort}
              onChange={(v) => set('mailPort', v)}
              placeholder={draft.mailProtocol === 'pop3' ? (draft.mailSecurity === 'tls' ? '995' : '110') : draft.mailSecurity === 'tls' ? '993' : '143'}
              mono
            />
          </Pair>
          <Pair>
            <Text
              label="Mailbox address (user name)"
              required
              value={draft.mailUsername}
              onChange={(v) => set('mailUsername', v)}
              placeholder="results-feed@hospital.example"
              mono
            />
            <Choose
              label="Sign in with"
              value={draft.mailAuth}
              onChange={(v) => set('mailAuth', v)}
              options={[
                { value: 'password', label: 'A password (an app password for Gmail)' },
                { value: 'oauth', label: 'OAuth 2.0 (Microsoft 365, Gmail)' },
              ]}
            />
          </Pair>
          {draft.mailAuth === 'password' ? (
            <Secret
              label="Password"
              value={draft.mailPassword}
              onChange={(v) => set('mailPassword', v)}
              hint="Prefer ${MAIL_PASSWORD}: the channel file goes into version control. Microsoft 365 has turned password sign-in off; use OAuth."
            />
          ) : (
            <>
              <Pair>
                <Choose
                  label="Provider"
                  value={draft.mailOAuthProvider}
                  onChange={(v) => set('mailOAuthProvider', v)}
                  options={[
                    { value: 'microsoft', label: 'Microsoft 365 (Entra ID)' },
                    { value: 'google', label: 'Google (Gmail)' },
                    { value: 'custom', label: 'Another provider' },
                  ]}
                />
                {draft.mailOAuthProvider === 'microsoft' ? (
                  <Text
                    label="Tenant"
                    value={draft.mailOAuthTenant}
                    onChange={(v) => set('mailOAuthTenant', v)}
                    placeholder="contoso.onmicrosoft.com"
                    mono
                    hint="The directory (tenant) id or domain. Empty uses organizations."
                  />
                ) : draft.mailOAuthProvider === 'custom' ? (
                  <Text
                    label="Token URL"
                    required
                    value={draft.mailOAuthTokenUrl}
                    onChange={(v) => set('mailOAuthTokenUrl', v)}
                    placeholder="https://idp.example/oauth2/token"
                    mono
                  />
                ) : (
                  <span />
                )}
              </Pair>
              <Pair>
                <Text label="Client id" required value={draft.mailOAuthClientId} onChange={(v) => set('mailOAuthClientId', v)} mono />
                <Secret
                  label="Client secret"
                  value={draft.mailOAuthClientSecret}
                  onChange={(v) => set('mailOAuthClientSecret', v)}
                  hint="Or ${MAIL_CLIENT_SECRET}. With no refresh token, Microsoft 365 app-only access is used (an administrator grants the app this one mailbox)."
                />
              </Pair>
              <Secret
                label="Refresh token"
                value={draft.mailOAuthRefreshToken}
                onChange={(v) => set('mailOAuthRefreshToken', v)}
                hint="A person's consent to the mailbox, as ${MAIL_REFRESH_TOKEN}. Required for Gmail, which has no app-only access."
              />
            </>
          )}
          <Pair>
            <Choose
              label="What becomes a message"
              value={draft.mailTake}
              onChange={(v) => set('mailTake', v)}
              options={[
                { value: 'attachments', label: 'Each attachment' },
                { value: 'body', label: 'The text of the email' },
                { value: 'raw', label: 'The whole email, for a script to take apart' },
              ]}
            />
            {draft.mailTake === 'attachments' ? (
              <Text
                label="Only attachments named"
                value={draft.mailAttachmentNames}
                onChange={(v) => set('mailAttachmentNames', v)}
                placeholder="*.hl7, *.csv"
                mono
                hint="Patterns, comma-separated. Empty takes every attachment."
              />
            ) : (
              <span />
            )}
          </Pair>
          <Text
            label="Only mail from"
            value={draft.mailAllowedSenders}
            onChange={(v) => set('mailAllowedSenders', v)}
            placeholder="results@lab.example, partner.example"
            mono
            hint="Addresses and domains, comma-separated. A From line can be forged: this keeps out accidents and stray mail, not an attacker."
          />
          {draft.mailProtocol === 'imap' && (
            <>
              <Pair>
                <Text label="Folder to read" value={draft.mailMailbox} onChange={(v) => set('mailMailbox', v)} placeholder="INBOX" mono />
                <Choose
                  label="After an email is handled"
                  value={draft.mailAfterRead}
                  onChange={(v) => set('mailAfterRead', v)}
                  options={[
                    { value: 'move', label: 'Move it to another folder' },
                    { value: 'flag', label: 'Mark it read' },
                    { value: 'delete', label: 'Delete it' },
                  ]}
                />
              </Pair>
              <Pair>
                {draft.mailAfterRead === 'move' ? (
                  <Text label="Move it to" value={draft.mailMoveTo} onChange={(v) => set('mailMoveTo', v)} placeholder="Processed" mono />
                ) : (
                  <span />
                )}
                <Text
                  label="Problem mail goes to"
                  value={draft.mailErrorFolder}
                  onChange={(v) => set('mailErrorFolder', v)}
                  placeholder="Error"
                  mono
                  hint="Mail that failed, or that was not taken (a sender not allowed, no matching attachment)."
                />
              </Pair>
            </>
          )}
          <Num
            label="Check every, seconds"
            value={draft.mailPollSeconds}
            onChange={(v) => set('mailPollSeconds', v ?? 60)}
            placeholder="60"
            hint="At least 10: mail services throttle accounts that check faster."
          />
        </>
      )}

      {draft.sourceKind === 'mqtt' && (
        <>
          <Pair>
            <Text
              label="Broker address"
              required
              value={draft.mqttSrcAddr}
              onChange={(v) => set('mqttSrcAddr', v)}
              placeholder="broker.hospital.local:8883"
              mono
              hint="host:port. 8883 is MQTT over TLS, 1883 plain."
            />
            <Text
              label="Topic filter"
              required
              value={draft.mqttSrcTopic}
              onChange={(v) => set('mqttSrcTopic', v)}
              placeholder="hl7/+/adt"
              mono
              hint="+ matches one level and # every level below, e.g. lab/#."
            />
          </Pair>
          <Pair>
            <Text label="Username" value={draft.mqttSrcUsername} onChange={(v) => set('mqttSrcUsername', v)} mono />
            <Secret
              label="Password"
              value={draft.mqttSrcPassword}
              onChange={(v) => set('mqttSrcPassword', v)}
              hint="Or ${MQTT_PASSWORD} to read it from the environment."
            />
          </Pair>
          <Pair>
            <Text
              label="Client id"
              value={draft.mqttSrcClientId}
              onChange={(v) => set('mqttSrcClientId', v)}
              placeholder="perfuse-<channel name>"
              mono
              hint="The broker keeps messages for this id while Perfuse is away. Keep it unique and unchanged."
            />
            <Choose
              label="Quality of service"
              value={draft.mqttSrcQos}
              onChange={(v) => set('mqttSrcQos', v)}
              options={[
                { value: '1', label: 'At least once (QoS 1)' },
                { value: '0', label: 'At most once (QoS 0): may lose messages' },
              ]}
            />
          </Pair>
          <Check
            label="Encrypt the connection with TLS"
            value={draft.mqttSrcTls}
            onChange={(v) => set('mqttSrcTls', v)}
            hint="A message is acknowledged only after this channel has handled it. One that fails is not acknowledged; Perfuse reconnects and the broker sends it again."
          />
        </>
      )}

      {draft.sourceKind === 'ibm_mq' && (
        <>
          <Pair>
            <Text
              label="mqweb address"
              required
              value={draft.ibmSrcUrl}
              onChange={(v) => set('ibmSrcUrl', v)}
              placeholder="https://mq.hospital.local:9443"
              mono
              hint="The queue manager's web server, which serves the messaging REST API (IBM MQ 9.1.5 and later)."
            />
            <Choose
              label="REST API version"
              value={draft.ibmSrcApiVersion}
              onChange={(v) => set('ibmSrcApiVersion', v)}
              options={[
                { value: '', label: 'v3 (MQ 9.3 and later)' },
                { value: 'v2', label: 'v2 (MQ 9.1.5 to 9.2)' },
                { value: 'v1', label: 'v1' },
              ]}
            />
          </Pair>
          <Pair>
            <Text label="Queue manager" required value={draft.ibmSrcQmgr} onChange={(v) => set('ibmSrcQmgr', v)} placeholder="QM1" mono />
            <Text label="Queue" required value={draft.ibmSrcQueue} onChange={(v) => set('ibmSrcQueue', v)} placeholder="HL7.ADT.IN" mono />
          </Pair>
          <Pair>
            <Text
              label="Username"
              value={draft.ibmSrcUsername}
              onChange={(v) => set('ibmSrcUsername', v)}
              mono
              hint="An mqweb user with the MQWebUser role and get and browse authority on the queue."
            />
            <Secret
              label="Password"
              value={draft.ibmSrcPassword}
              onChange={(v) => set('ibmSrcPassword', v)}
              hint="Or ${IBM_MQ_PASSWORD}."
            />
          </Pair>
          <Num
            label="When the queue is empty, look again every, seconds"
            value={draft.ibmSrcPollSeconds}
            onChange={(v) => set('ibmSrcPollSeconds', v ?? 2)}
            placeholder="2"
            hint="Each message is read, handled, and only then removed. One that fails stays first on the queue."
          />
        </>
      )}

      {draft.sourceKind === 'pubsub' && (
        <>
          <Pair>
            <Text label="Project" required value={draft.psSrcProject} onChange={(v) => set('psSrcProject', v)} placeholder="hospital-integration" mono />
            <Text
              label="Subscription"
              required
              value={draft.psSrcSubscription}
              onChange={(v) => set('psSrcSubscription', v)}
              placeholder="adt-to-perfuse"
              mono
              hint="A pull subscription. Turn on message ordering there to keep one patient's messages in order."
            />
          </Pair>
          <Pair>
            <Text
              label="Service account key file"
              value={draft.psSrcCredentialsFile}
              onChange={(v) => set('psSrcCredentialsFile', v)}
              placeholder="/etc/perfuse/gcp-key.json"
              mono
              hint="Empty on Google Cloud: the machine's own service account is used. It needs the Pub/Sub Subscriber role."
            />
            <Text
              label="Messages per pull"
              value={draft.psSrcMaxMessages}
              onChange={(v) => set('psSrcMaxMessages', v)}
              placeholder="10"
              hint="Still handled one at a time, in order."
            />
          </Pair>
          <Text
            label="Endpoint"
            value={draft.psSrcEndpoint}
            onChange={(v) => set('psSrcEndpoint', v)}
            placeholder="Leave empty for Google"
            mono
            hint="Only for the Pub/Sub emulator (http://127.0.0.1:8085) or a regional or private endpoint."
          />
        </>
      )}

      {draft.sourceKind === 'gcs' && (
        <>
          <Pair>
            <Text label="Bucket" required value={draft.gcsSrcBucket} onChange={(v) => set('gcsSrcBucket', v)} mono />
            <Text
              label="Service account key file"
              value={draft.gcsSrcCredentialsFile}
              onChange={(v) => set('gcsSrcCredentialsFile', v)}
              placeholder="/etc/perfuse/gcp-key.json"
              mono
              hint="Empty on Google Cloud: the machine's own service account is used."
            />
          </Pair>
          <Pair>
            <Text
              label="Prefix to read"
              required
              value={draft.gcsSrcPrefix}
              onChange={(v) => set('gcsSrcPrefix', v)}
              placeholder="inbound/"
              mono
            />
            <Text label="Only names ending in" value={draft.gcsSrcSuffix} onChange={(v) => set('gcsSrcSuffix', v)} placeholder=".hl7" mono />
          </Pair>
          <Pair>
            <Choose
              label="After an object is handled"
              value={draft.gcsSrcAfterRead}
              onChange={(v) => set('gcsSrcAfterRead', v)}
              options={[
                { value: 'move', label: 'Move it to another prefix' },
                { value: 'delete', label: 'Delete it' },
              ]}
            />
            <Num
              label="Poll every, seconds"
              value={draft.gcsSrcPollSeconds}
              onChange={(v) => set('gcsSrcPollSeconds', v ?? 30)}
              placeholder="30"
            />
          </Pair>
          <Pair>
            {draft.gcsSrcAfterRead === 'move' && (
              <Text label="Move to" value={draft.gcsSrcMoveTo} onChange={(v) => set('gcsSrcMoveTo', v)} placeholder="processed/" mono />
            )}
            <Text
              label="Failed objects go to"
              value={draft.gcsSrcErrorPrefix}
              onChange={(v) => set('gcsSrcErrorPrefix', v)}
              placeholder="error/"
              mono
            />
          </Pair>
          <Check
            label="Each object holds several MLLP-framed messages"
            value={draft.gcsSrcFramed}
            onChange={(v) => set('gcsSrcFramed', v)}
          />
          <Text
            label="Endpoint"
            value={draft.gcsSrcEndpoint}
            onChange={(v) => set('gcsSrcEndpoint', v)}
            placeholder="Leave empty for Google"
            mono
            hint="Only for an emulator such as fake-gcs-server, or a private endpoint."
          />
        </>
      )}

      {(draft.sourceKind === 'amqp' || draft.sourceKind === 'mqtt' || draft.sourceKind === 'ibm_mq') &&
        (draft.sourceKind === 'amqp' ? draft.amqpTls : draft.sourceKind === 'mqtt' ? draft.mqttSrcTls : draft.ibmSrcUrl.startsWith('https://')) && (
          <Pair>
            <Text
              label="CA certificate file"
              value={draft.connTlsCaFile}
              onChange={(v) => set('connTlsCaFile', v)}
              placeholder="/etc/perfuse/broker-ca.pem"
              mono
              hint="Only for a server certificate from a private CA. Empty trusts the system's certificate store."
            />
            <Text
              label="Client certificate file"
              value={draft.connTlsCertFile}
              onChange={(v) => set('connTlsCertFile', v)}
              placeholder="/etc/perfuse/client.pem"
              mono
              hint="Only when the server asks for one, as AWS IoT Core does."
            />
          </Pair>
        )}
      {(draft.sourceKind === 'amqp' || draft.sourceKind === 'mqtt' || draft.sourceKind === 'ibm_mq') && draft.connTlsCertFile && (
        <Text
          label="Client key file"
          value={draft.connTlsKeyFile}
          onChange={(v) => set('connTlsKeyFile', v)}
          placeholder="/etc/perfuse/client.key"
          mono
        />
      )}

      {draft.sourceKind === 'kafka' && (
        <>
          <Pair>
            <Text
              label="Bootstrap servers"
              required
              value={draft.kafkaBrokers}
              onChange={(v) => set('kafkaBrokers', v)}
              placeholder="kafka-1.hospital.local:9092, kafka-2.hospital.local:9092"
              mono
              hint="Comma separated. Give more than one: a single bootstrap address is a single point of failure for starting up, and the cluster survives losing it when this channel would not."
            />
            <Text
              label="Topics"
              required
              value={draft.kafkaTopics}
              onChange={(v) => set('kafkaTopics', v)}
              placeholder="adt.events, orders.inbound"
              mono
              hint="Comma separated. One channel can read several topics."
            />
          </Pair>

          <Pair>
            <Text
              label="Consumer group"
              required
              value={draft.kafkaGroup}
              onChange={(v) => set('kafkaGroup', v)}
              placeholder="perfuse-adt"
              mono
              hint="Required, not defaulted, because this is what remembers how far this channel has read. A generated name would start over on every restart and leave the old group holding offsets nobody reads. Two channels sharing a group split the traffic between them, which looks like messages going missing."
            />
            <Text
              label="Session timeout"
              value={draft.kafkaSessionTimeout}
              onChange={(v) => set('kafkaSessionTimeout', v)}
              placeholder="45s"
              mono
              hint="How long the cluster waits before deciding this consumer is gone and giving its partitions to someone else."
            />
          </Pair>

          <Check
            label="Commit only after a message has been handled"
            value={draft.kafkaCommitAfterDelivery}
            onChange={(v) => set('kafkaCommitAfterDelivery', v)}
            hint="Leave this on. With it on, a crash redelivers the message — a duplicate, which receivers are built to absorb. With it off, a crash between the commit and the delivery loses the message silently, with nothing anywhere recording that it existed."
          />

          <Check
            label="Read the topic from the beginning when the group is new"
            value={draft.kafkaFromBeginning}
            onChange={(v) => set('kafkaFromBeginning', v)}
            hint="Off by default on purpose. A topic with two years of history would replay two years of patient events into whatever this channel feeds, on the day it is switched on."
          />

          <Advanced
            title="Authentication"
            open={showKafkaSasl}
            onToggle={() => setShowKafkaSasl((v) => !v)}
          >
            <Pair>
              <Choose
                label="Mechanism"
                value={draft.kafkaSaslMechanism}
                onChange={(v) => set('kafkaSaslMechanism', v)}
                options={[
                  { value: '', label: 'None' },
                  { value: 'plain', label: 'PLAIN' },
                  { value: 'scram-sha-256', label: 'SCRAM-SHA-256' },
                  { value: 'scram-sha-512', label: 'SCRAM-SHA-512' },
                ]}
                hint="PLAIN sends the password readable on the wire, so pair it with TLS. SCRAM does not."
              />
              <Text
                label="Username"
                value={draft.kafkaSaslUsername}
                onChange={(v) => set('kafkaSaslUsername', v)}
              />
            </Pair>
            <Pair>
              <Secret
                label="Password"
                value={draft.kafkaSaslPassword}
                onChange={(v) => set('kafkaSaslPassword', v)}
              />
            </Pair>
          </Advanced>
        </>
      )}

      {draft.sourceKind === 'broker' && (
        <>
          <Pair>
            <Text
              label="Broker address"
              required
              value={draft.brokerAddr}
              onChange={(v) => set('brokerAddr', v)}
              placeholder="broker.hospital.local:61613"
              mono
              hint="The STOMP port, which is 61613 by default — not the broker's native port. JMS is a Java API rather than a protocol, so STOMP is what crosses the wire."
            />
            <Text
              label="Queue or topic"
              required
              value={draft.brokerDestination}
              onChange={(v) => set('brokerDestination', v)}
              placeholder="/queue/hl7.inbound"
              mono
              hint="Passed through exactly as typed. Brokers spell these differently — /queue/name on ActiveMQ, a bare name on RabbitMQ — so guessing would work against one and silently read nothing on another."
            />
          </Pair>
          <Pair>
            <Text label="Username" value={draft.brokerLogin} onChange={(v) => set('brokerLogin', v)} />
            <Secret
              label="Password"
              value={draft.brokerPasscode}
              onChange={(v) => set('brokerPasscode', v)}
            />
          </Pair>
          <Pair>
            <Text
              label="Subscription name"
              value={draft.brokerSubscriptionID}
              onChange={(v) => set('brokerSubscriptionID', v)}
              mono
              hint="Defaults to the channel name. A durable subscription is identified by this, so a changing one creates a new subscription on every restart and leaves the old ones accumulating messages nobody reads."
            />
            <Text
              label="Only messages matching"
              value={draft.brokerSelector}
              onChange={(v) => set('brokerSelector', v)}
              mono
              hint="A JMS selector, applied at the broker. On a shared queue that is the difference between reading a hundred messages a day and a hundred thousand."
            />
          </Pair>

          <Pair>
            <Text
              label="Heartbeat"
              value={draft.brokerHeartbeat}
              onChange={(v) => set('brokerHeartbeat', v)}
              placeholder="30s"
              mono
              hint="How often each side proves it is still there. Without one, a connection cut by a firewall or a load balancer looks alive from here indefinitely - the socket stays open and no messages arrive, which is the quietest possible failure."
            />
            <Text
              label="Wait before reconnecting"
              value={draft.brokerReconnect}
              onChange={(v) => set('brokerReconnect', v)}
              placeholder="5s"
              mono
              hint="After a dropped connection. Too short and a broker that is restarting is hammered while it comes up; too long and messages queue at the broker for no reason."
            />
          </Pair>
        </>
      )}

      {draft.sourceKind === 'file' && <LocalFileFields draft={draft} set={set} />}
      {draft.sourceKind === 'ftp' && <FTPSourceFields draft={draft} set={set} />}
      {draft.sourceKind === 'smb' && <SMBSourceFields draft={draft} set={set} />}
      {draft.sourceKind === 'webdav' && <WebDAVSourceFields draft={draft} set={set} />}
      {draft.sourceKind === 'tcp' && <TCPSourceFields draft={draft} set={set} />}
      {draft.sourceKind === 'serial' && <SerialSourceFields draft={draft} set={set} />}

      {draft.sourceKind === 'sftp' && (
        <>
          <Pair>
            <Text
              label="Server"
              required
              value={draft.sftpHost}
              onChange={(v) => set('sftpHost', v)}
              placeholder="sftp.example.org:22"
              mono
            />
            <Text
              label="Username"
              required
              value={draft.sftpUser}
              onChange={(v) => set('sftpUser', v)}
            />
          </Pair>
          <Pair>
            <Text
              label="Private key file"
              value={draft.sftpKeyFile}
              onChange={(v) => set('sftpKeyFile', v)}
              mono
              hint="Preferable to a password. Only the path is stored; the key itself stays on disk."
            />
            <Secret
              label="Password, if there is no key"
              value={draft.sftpPassword}
              onChange={(v) => set('sftpPassword', v)}
            />
          </Pair>
          <Pair>
            <Secret
              label="Passphrase for the key"
              value={draft.sftpKeyPassphrase}
              onChange={(v) => set('sftpKeyPassphrase', v)}
              hint="Only if the key file is encrypted. Without it, an encrypted key fails at connection time with a message about authentication, which sends people looking at the username."
            />
            <Text
              label="Largest file accepted (bytes)"
              value={draft.sftpMaxFileSize}
              onChange={(v) => set('sftpMaxFileSize', v)}
              placeholder="0"
              mono
              hint="Zero or empty means no limit. A partner that accidentally uploads a database dump into the drop directory is otherwise read into memory in full."
            />
          </Pair>
          <Text
            label="Known hosts file"
            value={draft.sftpKnownHosts}
            onChange={(v) => set('sftpKnownHosts', v)}
            mono
            hint="Confirms the server is the one you think it is. Without it the connection is refused rather than trusted blindly."
          />
          <Pair>
            <Text
              label="Directory to watch"
              required
              value={draft.sftpDir}
              onChange={(v) => set('sftpDir', v)}
              mono
            />
            <Text
              label="Only files matching"
              value={draft.sftpPattern}
              onChange={(v) => set('sftpPattern', v)}
              placeholder="*.hl7"
              mono
            />
          </Pair>
          <Text
            label="Move each file here once it has been read"
            required
            value={draft.sftpMoveTo}
            onChange={(v) => set('sftpMoveTo', v)}
            placeholder="/outbound/done"
            mono
            hint="A file left where it was found is read again on the next poll, so every message in it is delivered twice, then three times. This is why it is required rather than optional."
          />
          <Pair>
            <Num
              label="Check for new files every (seconds)"
              value={draft.sftpPollSeconds}
              onChange={(v) => set('sftpPollSeconds', v ?? 0)}
            />
            <Num
              label="Wait for a file to stop changing for (seconds)"
              value={draft.sftpStableSeconds}
              onChange={(v) => set('sftpStableSeconds', v ?? 0)}
              hint="Stops a half-finished upload being read as a truncated message."
            />
          </Pair>
        </>
      )}

      {draft.sourceKind === 'soap' && (
        <>
          <Choose
            label="Service"
            value={draft.soapSourceProfile}
            onChange={(v) => {
              set('soapSourceProfile', v)
              // The CDC WSDL binds SOAP 1.2.
              if (v === 'cdc-iis') set('soapSourceVersion', '1.2')
            }}
            options={[
              { value: '', label: 'Any SOAP caller, as configured below' },
              { value: 'cdc-iis', label: 'Answer as an immunization registry (CDC IIS web service)' },
            ]}
            hint="The registry interface answers connectivityTest, takes the message from submitSingleMessage, returns the channel's acknowledgement in return and serves the CDC WSDL at ?wsdl. Use it for a registry test double, or to receive submissions the way registries do."
          />

          <Pair>
            <Text
              label="Listen on"
              value={draft.soapSourceListen}
              onChange={(v) => set('soapSourceListen', v)}
              placeholder="0.0.0.0:8090"
              hint="Bind to 0.0.0.0 rather than 127.0.0.1 or nothing on the network can call it."
            />
            <Text
              label="Path"
              value={draft.soapSourcePath}
              onChange={(v) => set('soapSourcePath', v)}
              placeholder="/PatientFeed"
              hint="Whatever the calling system was told to post to. It appears in the WSDL Perfuse serves."
            />
          </Pair>

          <Pair>
            <Text
              label="SOAP version"
              value={draft.soapSourceVersion}
              onChange={(v) => set('soapSourceVersion', v as '1.1' | '1.2')}
              placeholder="1.1"
              hint="1.1 unless the caller says otherwise. The difference is not cosmetic — the two use different
              content types, and a mismatch is rejected before your message is looked at."
            />
            {draft.soapSourceProfile !== 'cdc-iis' && (
              <Text
                label="Element holding the message"
                value={draft.soapSourceElement}
                onChange={(v) => set('soapSourceElement', v)}
                placeholder="HL7Message"
                hint="The element inside the envelope body that contains the HL7. Namespace prefixes are ignored,
                so give the local name only — callers disagree about prefixes and none of them are wrong."
              />
            )}
          </Pair>

          {draft.soapSourceProfile === 'cdc-iis' ? (
            <Pair>
              <Text
                label="Username callers send"
                value={draft.soapSourceUsername}
                onChange={(v) => set('soapSourceUsername', v)}
                hint="Checked against the username inside submitSingleMessage, where the CDC WSDL puts it. A wrong one is answered with a SecurityFault. Left empty, any caller is accepted."
              />
              <Secret
                label="Password callers send"
                value={draft.soapSourcePassword}
                onChange={(v) => set('soapSourcePassword', v)}
              />
            </Pair>
          ) : (
          <Check
            label="The message arrives base64 encoded"
            value={draft.soapSourceBase64}
            onChange={(v) => set('soapSourceBase64', v)}
            hint="Common, because it stops the segment separators being mangled by XML. Turn it on if the caller
            says they encode — with it off, an encoded message arrives as one long unparseable line."
          />
          )}

          {draft.soapSourceProfile !== 'cdc-iis' && (

          <Advanced
            title="Response and authentication"
            open={showSoapAdvanced}
            onToggle={() => setShowSoapAdvanced((v) => !v)}
            hint="An endpoint with neither a token nor a username accepts messages from anyone who can reach the port."
          >
            <Text
              label="Response element"
              value={draft.soapSourceResponseElement}
              onChange={(v) => set('soapSourceResponseElement', v)}
              placeholder="HL7ACK"
              hint="What to wrap the acknowledgement in. The caller's client is usually generated from a WSDL and
              will reject a name it does not expect."
            />
            <Text
              label="Response namespace"
              value={draft.soapSourceResponseNamespace}
              onChange={(v) => set('soapSourceResponseNamespace', v)}
              placeholder="urn:hl7-org:v2xml"
              mono
              hint="The namespace the response element sits in. A generated client validates against both, so the right element in the wrong namespace is rejected as though the acknowledgement never came - and the sender then retries a message that was accepted."
            />
            <Text
              label="WSDL to publish"
              value={draft.soapSourceWSDL}
              onChange={(v) => set('soapSourceWSDL', v)}
              placeholder="/etc/perfuse/inbound.wsdl"
              mono
              hint="A file served at this endpoint's address with ?wsdl. Callers generate their client from it, so publishing one is usually the difference between a partner integrating in an afternoon and exchanging emails for a week."
            />
            <Check
              label="Answer a rejected message with a SOAP fault"
              value={draft.soapSourceFaultOnNak}
              onChange={(v) => set('soapSourceFaultOnNak', v)}
              hint="Off means a rejection comes back as a normal response containing a negative acknowledgement, which many generated clients treat as success. On means a fault, which they treat as an error. Which one is right depends on the caller, and getting it wrong means a rejected message looks delivered to them."
            />
            <Secret
              label="Bearer token"
              value={draft.soapSourceToken}
              onChange={(v) => set('soapSourceToken', v)}
              hint="Without a token or a username, anybody who can reach the port can send messages into this
              channel."
            />
            <Pair>
              <Text
                label="Username"
                value={draft.soapSourceUsername}
                onChange={(v) => set('soapSourceUsername', v)}
              />
              <Secret
                label="Password"
                value={draft.soapSourcePassword}
                onChange={(v) => set('soapSourcePassword', v)}
              />
            </Pair>
          </Advanced>
          )}
        </>
      )}

      {draft.sourceKind === 'as2' && (
        <>
          <Pair>
            <Text
              label="Listen on"
              value={draft.as2SrcListen}
              onChange={(v) => set('as2SrcListen', v)}
              placeholder=":4080"
              mono
              hint="Partners post to this address. Give them the full URL, with the path below."
            />
            <Text
              label="Path"
              value={draft.as2SrcPath}
              onChange={(v) => set('as2SrcPath', v)}
              placeholder="/as2"
              mono
            />
          </Pair>
          <Text
            label="Our AS2 identifier"
            value={draft.as2SrcId}
            onChange={(v) => set('as2SrcId', v)}
            placeholder="HOSPITAL-AS2"
            mono
            hint="Partners send it in AS2-To. A message addressed to anything else is refused."
          />
          <Pair>
            <Text
              label="Our certificate (PEM)"
              value={draft.as2SrcCertFile}
              onChange={(v) => set('as2SrcCertFile', v)}
              placeholder="/etc/perfuse/as2/ours.crt"
              mono
              hint="Give it to each partner: they encrypt to it and check our receipts' signatures with it."
            />
            <Text
              label="Our private key (PEM)"
              value={draft.as2SrcKeyFile}
              onChange={(v) => set('as2SrcKeyFile', v)}
              placeholder="/etc/perfuse/as2/ours.key"
              mono
            />
          </Pair>
          <Pair>
            <Text
              label="Remember Message-IDs for"
              value={draft.as2SrcDuplicateWindow}
              onChange={(v) => set('as2SrcDuplicateWindow', v)}
              placeholder="720h"
              mono
              hint="A partner that never got our receipt sends the message again with the same Message-ID; within this time it gets a receipt and is not passed on twice. Hours, such as 720h (30 days, the default). Kept in the database, so a restart does not forget."
            />
            <Text
              label="Largest message (bytes)"
              value={draft.as2SrcMaxMessageSize}
              onChange={(v) => set('as2SrcMaxMessageSize', v.replace(/[^0-9]/g, ''))}
              placeholder="52428800"
              mono
              hint="Empty: 50 MB. A compressed message may expand to ten times this."
            />
          </Pair>
          <div className="space-y-2 rounded-lg border border-slate-800 p-3" data-testid="as2-partners">
            <p className="text-sm text-slate-300">
              <span className="text-sky-300">Trading partners.</span> Only these may send here. Each must sign and encrypt unless
              its agreement says otherwise.
            </p>
            {draft.as2SrcPartners.length === 0 && (
              <p className="rounded border border-rose-700 bg-rose-950/40 px-2 py-1.5 text-xs text-rose-200">
                No partners yet, so every message would be refused.
              </p>
            )}
            {draft.as2SrcPartners.map((p, i) => {
              const change = (patch: Partial<typeof p>) =>
                set('as2SrcPartners', draft.as2SrcPartners.map((x, j) => (j === i ? { ...x, ...patch } : x)))
              return (
                <div key={i} className="grid gap-2 rounded border border-slate-800 p-2 sm:grid-cols-[1fr_2fr_auto]">
                  <input
                    aria-label={`Partner ${i + 1} AS2 identifier`}
                    className="input font-mono text-xs"
                    value={p.id}
                    onChange={(e) => change({ id: e.target.value })}
                    placeholder="PAYER-AS2"
                  />
                  <input
                    aria-label={`Partner ${i + 1} certificate`}
                    className="input font-mono text-xs"
                    value={p.certFile}
                    onChange={(e) => change({ certFile: e.target.value })}
                    placeholder="/etc/perfuse/as2/payer.crt"
                  />
                  <button
                    type="button"
                    className="btn-ghost py-1 text-xs text-rose-300"
                    onClick={() => set('as2SrcPartners', draft.as2SrcPartners.filter((_, j) => j !== i))}
                  >
                    Remove
                  </button>
                  <label className="flex items-center gap-2 text-xs text-slate-400 sm:col-span-3">
                    <input type="checkbox" checked={p.allowUnsigned} onChange={(e) => change({ allowUnsigned: e.target.checked })} />
                    accept unsigned messages
                    <input type="checkbox" className="ml-4" checked={p.allowUnencrypted} onChange={(e) => change({ allowUnencrypted: e.target.checked })} />
                    accept unencrypted messages
                  </label>
                  {!p.certFile.trim() && !p.allowUnsigned && (
                    <p className="text-xs text-rose-300 sm:col-span-3">Its certificate is needed to check its signatures.</p>
                  )}
                </div>
              )
            })}
            <button
              type="button"
              className="btn-ghost py-1 text-xs"
              onClick={() =>
                set('as2SrcPartners', [...draft.as2SrcPartners, { id: '', certFile: '', allowUnsigned: false, allowUnencrypted: false }])
              }
            >
              Add a partner
            </button>
          </div>
          <Check
            label="Serve HTTPS"
            value={draft.as2SrcTlsEnabled}
            onChange={(v) => set('as2SrcTlsEnabled', v)}
            hint="The payload is encrypted either way; most partner agreements require HTTPS as well."
          />
          {draft.as2SrcTlsEnabled && (
            <Pair>
              <Text label="HTTPS certificate (PEM)" value={draft.as2SrcTlsCertFile} onChange={(v) => set('as2SrcTlsCertFile', v)} mono />
              <Text label="HTTPS key (PEM)" value={draft.as2SrcTlsKeyFile} onChange={(v) => set('as2SrcTlsKeyFile', v)} mono />
            </Pair>
          )}
          <p className="rounded-lg border border-teal-800/60 bg-teal-950/20 p-3 text-xs leading-relaxed text-teal-200">
            Each message is decrypted and its signature checked, then handled as the channel's message format says (X12 or EDIFACT
            usually). The partner gets a receipt, signed if it asked, naming the digest of what arrived, or why it was refused. This
            source also takes the asynchronous receipts partners send for messages an AS2 destination here sent.
          </p>
        </>
      )}

      {draft.sourceKind === 'direct' && (
        <>
          <Pair>
            <Text
              label="Direct address"
              value={draft.directSrcAddress}
              onChange={(v) => set('directSrcAddress', v)}
              placeholder="intake@direct.clinic.example"
              mono
              hint="Mail for any other address is refused at RCPT, so the HISP reports it rather than Perfuse dropping it."
            />
            <Text
              label="Listen on (SMTP)"
              value={draft.directSrcListen}
              onChange={(v) => set('directSrcListen', v)}
              placeholder="0.0.0.0:2525"
              mono
              hint="The address the HISP delivers this address's mail to. Ask the HISP which source addresses to allow. Leave it empty when mail is read from the HISP's mailbox or taken as XDR below."
            />
          </Pair>
          <Pair>
            <Text
              label="Address certificate (PEM)"
              value={draft.directSrcCertFile}
              onChange={(v) => set('directSrcCertFile', v)}
              placeholder="/etc/perfuse/direct/intake.crt"
              mono
              hint="Issued by the HISP or its registration authority. Mail encrypted to it is opened with the key below."
            />
            <Text
              label="Address private key (PEM)"
              value={draft.directSrcKeyFile}
              onChange={(v) => set('directSrcKeyFile', v)}
              placeholder="/etc/perfuse/direct/intake.key"
              mono
            />
          </Pair>
          <Text
            label="Trust anchors (PEM)"
            value={draft.directSrcAnchorsFile}
            onChange={(v) => set('directSrcAnchorsFile', v)}
            placeholder="/etc/perfuse/direct/anchors.pem"
            mono
            hint="The certificate authorities whose senders you accept, such as the DirectTrust bundle. A message signed by anyone else, or signed by a certificate that is not for its From address, is refused."
          />
          <Advanced
            title="STARTTLS, notifications and limits"
            open={showDirectAdvanced}
            onToggle={() => setShowDirectAdvanced((v) => !v)}
          >
            <Pair>
              <Text
                label="SMTP TLS certificate"
                value={draft.directSrcTlsCert}
                onChange={(v) => set('directSrcTlsCert', v)}
                mono
                hint="With a certificate and key, STARTTLS is offered and required before mail is taken."
              />
              <Text label="SMTP TLS key" value={draft.directSrcTlsKey} onChange={(v) => set('directSrcTlsKey', v)} mono />
            </Pair>
            <Pair>
              <Text
                label="HISP submission server (for notifications)"
                value={draft.directSrcSmtpHost}
                onChange={(v) => set('directSrcSmtpHost', v)}
                placeholder="smtp.hisp.example:587"
                mono
                hint="Set it and each message taken in is answered with a signed, encrypted 'processed' notification (MDN) to its sender."
              />
              <Num label="Largest message (bytes)" value={draft.directSrcMaxBytes} onChange={(v) => set('directSrcMaxBytes', v ?? 0)} hint="64 MB when empty." />
            </Pair>
            <Pair>
              <Text label="HISP username" value={draft.directSrcUsername} onChange={(v) => set('directSrcUsername', v)} />
              <Secret label="HISP password" value={draft.directSrcPassword} onChange={(v) => set('directSrcPassword', v)} />
            </Pair>
          </Advanced>
          <Advanced
            title="Read the address's mailbox at the HISP (IMAP)"
            open={showDirectImap || !!draft.directSrcImapHost}
            onToggle={() => setShowDirectImap((v) => !v)}
          >
            <Pair>
              <Text
                label="IMAP server"
                value={draft.directSrcImapHost}
                onChange={(v) => set('directSrcImapHost', v)}
                placeholder="imap.hisp.example"
                mono
                hint="For a HISP that keeps the address's mail in a mailbox rather than delivering it by SMTP. Each message is opened as Direct; one that cannot be moves to the error folder."
              />
              <Num label="Port" value={draft.directSrcImapPort} onChange={(v) => set('directSrcImapPort', v ?? 0)} hint="993 when empty (143 with STARTTLS)." />
            </Pair>
            <Pair>
              <Choose
                label="Connection"
                value={draft.directSrcImapSecurity || 'tls'}
                onChange={(v) => set('directSrcImapSecurity', v === 'tls' ? '' : v)}
                options={[
                  { value: 'tls', label: 'TLS from the start' },
                  { value: 'starttls', label: 'STARTTLS' },
                ]}
              />
              <Text label="Mailbox folder" value={draft.directSrcImapMailbox} onChange={(v) => set('directSrcImapMailbox', v)} placeholder="INBOX" />
            </Pair>
            <Pair>
              <Text label="Mailbox username" value={draft.directSrcImapUsername} onChange={(v) => set('directSrcImapUsername', v)} />
              <Secret label="Mailbox password" value={draft.directSrcImapPassword} onChange={(v) => set('directSrcImapPassword', v)} />
            </Pair>
            <Pair>
              <Choose
                label="After a message is taken"
                value={draft.directSrcImapAfterRead || 'move'}
                onChange={(v) => set('directSrcImapAfterRead', v === 'move' ? '' : v)}
                options={[
                  { value: 'move', label: 'Move it to another folder' },
                  { value: 'delete', label: 'Delete it' },
                ]}
              />
              <Text label="Move it to" value={draft.directSrcImapMoveTo} onChange={(v) => set('directSrcImapMoveTo', v)} placeholder="Processed" />
            </Pair>
            <Pair>
              <Text label="Error folder" value={draft.directSrcImapErrorFolder} onChange={(v) => set('directSrcImapErrorFolder', v)} placeholder="Error" />
              <Text label="Check every" value={draft.directSrcImapPollInterval} onChange={(v) => set('directSrcImapPollInterval', v)} placeholder="60s" mono />
            </Pair>
          </Advanced>
          <Advanced title="Take XDR (IHE ITI-41 over SOAP)" open={showDirectXdr || !!draft.directSrcXdrListen} onToggle={() => setShowDirectXdr((v) => !v)}>
            <Text
              label="Listen on (HTTPS)"
              value={draft.directSrcXdrListen}
              onChange={(v) => set('directSrcXdrListen', v)}
              placeholder="0.0.0.0:8445"
              mono
              hint="Documents posted to /xdr there, as a HISP sends Direct converted to XDR, or a partner sends point to point. A submission whose Direct addressing block names another address is refused. The address's certificate and key are not needed for XDR alone: TLS protects it."
            />
            <Pair>
              <Text label="TLS certificate (PEM)" value={draft.directSrcXdrTlsCert} onChange={(v) => set('directSrcXdrTlsCert', v)} mono hint="Required unless it listens on 127.0.0.1." />
              <Text label="TLS key (PEM)" value={draft.directSrcXdrTlsKey} onChange={(v) => set('directSrcXdrTlsKey', v)} mono />
            </Pair>
            <Text
              label="Client certificate authorities (PEM)"
              value={draft.directSrcXdrClientCaFile}
              onChange={(v) => set('directSrcXdrClientCaFile', v)}
              mono
              hint="Set it and only a sender with a certificate from one of these may post: how a HISP or a partner usually authenticates XDR."
            />
          </Advanced>
          <p className="text-xs text-slate-400">
            Each message is decrypted and its signature checked before anything reads it. An XDM package&apos;s documents each become
            a message; otherwise each attachment does, or the text when there is none. Each document of an XDR submission becomes a
            message.
          </p>
        </>
      )}

      {draft.sourceKind === 'dicomweb' && (
        <>
          <Pair>
            <Text
              label="Listen on"
              value={draft.dicomwebListen}
              onChange={(v) => set('dicomwebListen', v)}
              placeholder="0.0.0.0:8043"
              hint="Senders post to this address followed by the service path and /studies."
            />
            <Text
              label="Service path"
              value={draft.dicomwebPath}
              onChange={(v) => set('dicomwebPath', v)}
              placeholder="/dicom-web"
              mono
              hint="Defaults to /dicom-web, the path Orthanc and dcm4chee use, so a sender set up for either needs only the host changed."
            />
          </Pair>
          <Pair>
            <Secret
              label="Bearer token"
              value={draft.dicomwebToken}
              onChange={(v) => set('dicomwebToken', v)}
              hint="Without a token or a username, anybody who can reach the port can send images into this channel."
            />
            <Text label="Username" value={draft.dicomwebUsername} onChange={(v) => set('dicomwebUsername', v)} />
          </Pair>
          <Pair>
            <Secret label="Password" value={draft.dicomwebPassword} onChange={(v) => set('dicomwebPassword', v)} />
            <Check
              label="Encrypt with TLS"
              value={draft.dicomwebTls}
              onChange={(v) => set('dicomwebTls', v)}
              hint="Images carry patient names. Set the certificate and key under Settings or in the file."
            />
          </Pair>
          {draft.dataType !== 'dicom' && (
            <p className="rounded-lg border border-amber-900/60 bg-amber-950/30 p-3 text-sm text-amber-200">
              This source receives imaging objects, so the channel&apos;s data type must be DICOM.
            </p>
          )}
        </>
      )}

      {draft.sourceKind === 'dicom' && (
        <Pair>
          <Text
            label="Listen on"
            value={draft.dicomListen}
            onChange={(v) => set('dicomListen', v)}
            placeholder="0.0.0.0:11112"
            hint="104 is the registered DICOM port and needs privileges on most systems; 11112 is the usual
            alternative. Bind to 0.0.0.0 rather than 127.0.0.1 or no scanner on the network can reach it."
          />
          <Text
            label="Our AE title"
            value={draft.dicomListenAe}
            onChange={(v) => set('dicomListenAe', v)}
            placeholder="PERFUSE"
            hint="Connections addressed to a different name are refused. Leaving it empty accepts any name,
            which is worth avoiding — on many sites the AE title is the only access control an imaging
            endpoint has."
          />
        </Pair>
      )}

      {draft.sourceKind === 'dicom' && (
        <>
          <Check
            label="Encrypt connections from scanners"
            value={draft.dicomListenTls}
            onChange={(v) => set('dicomListenTls', v)}
            hint="DICOM metadata carries patient names, so this is a compliance question rather than a
          preference. Many older modalities cannot do it, which is why it is a choice and not enforced."
          />

          <Advanced
            title="What this listener accepts"
            hint="Which senders, which kinds of image and how large. Left open, a C-STORE listener accepts anything from anyone that can reach the port."
            open={showDicomAdvanced}
            onToggle={() => setShowDicomAdvanced((v) => !v)}
          >
            <Text
              label="Only accept these calling AE titles"
              value={draft.dicomAllowedCallingAe}
              onChange={(v) => set('dicomAllowedCallingAe', v)}
              placeholder="CT01, MR02"
              mono
              hint="Comma separated. Empty means any host on the network may push images into a clinical channel. The called AE title above is the name a sender dials, so it identifies this listener rather than the sender — it is not a restriction."
            />

            <Text
              label="Only accept these SOP classes"
              value={draft.dicomSopClasses}
              onChange={(v) => set('dicomSopClasses', v)}
              placeholder="1.2.840.10008.5.1.4.1.1.2"
              mono
              hint="Comma separated UIDs. Empty accepts every kind of object, including ones this channel's destinations cannot handle. Refusing at the association is clearer to the sender than accepting and failing later."
            />

            <Text
              label="Only accept these transfer syntaxes"
              value={draft.dicomTransferSyntaxes}
              onChange={(v) => set('dicomTransferSyntaxes', v)}
              placeholder="1.2.840.10008.1.2.1"
              mono
              hint="Comma separated UIDs. Empty negotiates whatever the sender offers. Narrowing this is how you avoid receiving a compression nothing downstream can read."
            />

            <Text
              label="Largest object accepted (bytes)"
              value={draft.dicomMaxObjectBytes}
              onChange={(v) => set('dicomMaxObjectBytes', v)}
              placeholder="0"
              mono
              hint="Zero or empty means no limit. Whole-slide images and long series run to gigabytes, and the limit is otherwise memory."
            />
          </Advanced>
        </>
      )}

      {draft.sourceKind === 'dicom_query' && (
        <>
          <Pair>
            <Text
              label="Archive"
              value={draft.dicomQueryAddr}
              onChange={(v) => set('dicomQueryAddr', v)}
              placeholder="pacs.hospital.internal:104"
              hint="The PACS to ask. This is the connector Mirth has never had — it can receive images pushed
              at it, but not ask an archive what it holds."
            />
            <Text
              label="Its AE title"
              value={draft.dicomQueryCalledAe}
              onChange={(v) => set('dicomQueryCalledAe', v)}
              placeholder="PACS_MAIN"
              hint="Required in practice. Most archives refuse a connection addressed to anything else, and
              that refusal looks exactly like the archive being down."
            />
          </Pair>

          <Pair>
            <Text
              label="Our AE title"
              value={draft.dicomQueryCallingAe}
              onChange={(v) => set('dicomQueryCallingAe', v)}
              placeholder="PERFUSE"
              hint="What Perfuse calls itself. Archives commonly use this for access control, so their
              administrator will need to allow it."
            />
            <Text
              label="Ask every"
              value={draft.dicomQueryInterval}
              onChange={(v) => set('dicomQueryInterval', v)}
              placeholder="15m"
              hint="Each poll opens a connection, so anything under ten seconds is refused — from the
              archive's point of view that is indistinguishable from an attack."
            />
          </Pair>

          <Pair>
            <Text
              label="Look back"
              value={draft.dicomQueryWindow}
              onChange={(v) => set('dicomQueryWindow', v)}
              placeholder="72h"
              hint="How far back each poll asks. Must be at least the interval, or the gap between polls is
              never looked at and studies arriving in it are missed silently. Generous is safer than tight:
              already-seen studies are recognised and dropped."
            />
            <Text
              label="Level"
              value={draft.dicomQueryLevel}
              onChange={(v) => set('dicomQueryLevel', v as 'STUDY')}
              placeholder="STUDY"
              hint="STUDY gives one message per study, which is almost always what is wanted. IMAGE gives one
              per slice, so a single CT becomes hundreds of messages."
            />
          </Pair>

          <Pair>
            <Text
              label="Overlap each window by"
              value={draft.dicomQueryOverlap}
              onChange={(v) => set('dicomQueryOverlap', v)}
              placeholder="5m"
              mono
              hint="Extends each poll a little further back than the last one reached. Covers a study whose timestamp is set by the archive slightly after it became visible, which otherwise falls between two windows and is never collected."
            />
            <Check
              label="Query by patient rather than by study"
              value={draft.dicomQueryPatientRoot}
              onChange={(v) => set('dicomQueryPatientRoot', v)}
              hint="Changes which information model the query uses. Some archives support only one, and against the wrong one the query succeeds and returns nothing - which reads as an empty archive rather than as a mismatch."
            />
          </Pair>

          <Check
            label="Send everything found on the first poll"
            value={draft.dicomQueryEmitFirst}
            onChange={(v) => set('dicomQueryEmitFirst', v)}
            hint="Off by default, and the default is usually right: the first poll looks back over the whole window, so switching this on sends every study in it at once. On is for a deliberate backfill. Off means the first poll only records what exists, and collection starts from the next one."
          />

          <Area
            label="Bring back these fields"
            value={draft.dicomQueryReturn}
            onChange={(v) => set('dicomQueryReturn', v)}
            rows={4}
            placeholder={'PatientID\nPatientName\nAccessionNumber\nStudyDescription\nStudyInstanceUID'}
            hint="One name per line, as DICOM keywords rather than tag numbers. An unrecognised name is
            refused with the list of accepted ones, rather than being silently ignored by the archive — which
            would leave a query quietly matching more than intended."
          />

          <Check
            label="Send messages for studies that already exist on the first poll"
            value={draft.dicomQueryEmitFirst}
            onChange={(v) => set('dicomQueryEmitFirst', v)}
            hint="Leave this off against a real archive. Switched on, the first poll emits a message for
            everything it holds — which for a hospital PACS is years of studies, and the mistake is noticed
            downstream rather than here. Off, the first poll records what exists and reports only what is new
            afterwards."
          />

          <Check
            label="Encrypt the connection to the archive"
            value={draft.dicomQueryTls}
            onChange={(v) => set('dicomQueryTls', v)}
            hint="The responses carry patient names, so this matters as much here as on the images
            themselves."
          />
        </>
      )}

      {draft.sourceKind === 'javascript' && (
        <>
          <Area
            label="Script"
            value={draft.jsReaderScript}
            onChange={(v) => set('jsReaderScript', v)}
            hint="Return a string or an array of strings. Each string becomes one message in the channel."
          />
          <Pair>
            <Text
              label="Poll interval"
              value={draft.jsReaderInterval}
              onChange={(v) => set('jsReaderInterval', v)}
              hint="How often to run the script. e.g. 30s, 5m, 1h"
            />
            <Text
              label="Timeout"
              value={draft.jsReaderTimeout}
              onChange={(v) => set('jsReaderTimeout', v)}
              hint="Maximum time the script may run before being killed."
            />
          </Pair>
        </>
      )}

      {draft.sourceKind === 'database' && (
        <>
          <Pair>
            <Choose
              label="Database"
              value={draft.dbDriver}
              onChange={(v) => set('dbDriver', v)}
              options={[
                { value: 'postgres', label: 'PostgreSQL' },
                { value: 'mysql', label: 'MySQL or MariaDB' },
                { value: 'sqlserver', label: 'SQL Server' },
                { value: 'sqlite', label: 'SQLite' },
              ]}
            />
            <Num
              label="Check for new rows every (seconds)"
              value={draft.dbPollSeconds}
              onChange={(v) => set('dbPollSeconds', v ?? 0)}
            />
          </Pair>
          <Secret
            label="Connection string"
            value={draft.dbDSN}
            onChange={(v) => set('dbDSN', v)}
            hint="Kept in the channel file. It is never written to logs, metrics, traces or the channel summary, all of which redact it."
          />
          <Area
            label="Query that finds messages to send"
            value={draft.dbQuery}
            onChange={(v) => set('dbQuery', v)}
            placeholder="SELECT id, payload FROM outbound ORDER BY id"
            rows={2}
          />
          <Pair>
            <Text
              label="Column holding the whole message"
              value={draft.dbColumn}
              onChange={(v) => set('dbColumn', v)}
              mono
              hint="Use this when one column already contains a complete message."
            />
            <Text
              label="Column that identifies a row"
              value={draft.dbKeyColumn}
              onChange={(v) => set('dbKeyColumn', v)}
              mono
              hint="Remembered, so a row is not sent twice. Required unless you mark rows yourself below."
            />
          </Pair>
          <Area
            label="Or build a message from the columns"
            value={draft.dbTemplate}
            onChange={(v) => set('dbTemplate', v)}
            rows={3}
            hint="An alternative to the column above, for tables holding fields rather than whole messages."
          />
          <Area
            label="Run this after a row has been sent"
            value={draft.dbAfterQuery}
            onChange={(v) => set('dbAfterQuery', v)}
            placeholder="UPDATE outbound SET sent = now() WHERE id = :id"
            rows={2}
          />

          <Pair>
            <Text
              label="Give up on a query after"
              value={draft.dbQueryTimeout}
              onChange={(v) => set('dbQueryTimeout', v)}
              placeholder="30s"
              mono
              hint="A duration. Without one, a query that never returns holds the poll open indefinitely and the channel stops reading without failing - which looks like a quiet feed rather than a stuck one."
            />
            <Text
              label="Attempts per row"
              value={draft.dbMaxAttempts}
              onChange={(v) => set('dbMaxAttempts', v)}
              placeholder="3"
              mono
              hint="How many times one row is tried before it is left alone. Empty for the default. Too high and a single unparseable row is retried forever, which fills the log and hides everything else."
            />
          </Pair>
        </>
      )}

      {isX12 ? (
        <p className="rounded-lg border border-amber-800/50 bg-amber-950/20 p-3 text-xs leading-relaxed text-amber-300/90">
          X12 sends nothing back in the response. An acknowledgement is a 997 or 999, returned
          later as its own interchange, so there is no acknowledgement timing to choose here.
        </p>
      ) : (
        <Choose
          label="When should the sender be told we have the message?"
          value={draft.ackWhen}
          onChange={(v) => set('ackWhen', v)}
          options={[
            { value: 'on_delivery', label: 'once every destination has it (recommended)' },
            { value: 'on_receipt', label: 'as soon as we receive it' },
          ]}
          hint={
            draft.ackWhen === 'on_delivery'
              ? 'Safer. An acknowledgement means the message reached everywhere it was going, so it is a promise that was kept.'
              : 'Faster, and riskier. The sender is told the message is accepted straight away, which means an already-acknowledged message can be lost if this process stops with work still queued.'
          }
        />
      )}

      {!isX12 && (
        <Check
          label="Name the trigger event in the acknowledgement"
          value={draft.ackIncludeTriggerEvent}
          onChange={(v) => set('ackIncludeTriggerEvent', v)}
          hint="Puts the original trigger event in MSH-9 of the acknowledgement rather than leaving it as ACK alone. Some senders match a reply to its message on that field and discard anything else, which appears as an acknowledgement that never arrived."
        />
      )}

      <Advanced
        title="Connection limits"
        hint="rarely needed"
        open={showLimits}
        onToggle={() => setShowLimits((o) => !o)}
      >
        <Pair>
          <Num
            label="Close a silent connection after (seconds)"
            value={draft.idleTimeoutSeconds}
            onChange={(v) => set('idleTimeoutSeconds', v ?? 0)}
            placeholder="0 = never"
            hint="Never is usually right: a hospital feed holds one connection open for months and goes quiet overnight."
          />
          <Num
            label="Most connections at once"
            value={draft.maxConnections}
            onChange={(v) => set('maxConnections', v ?? 0)}
            placeholder="0 = no limit"
          />
        </Pair>
      </Advanced>
    </div>
  )
}

export function FormatSection({
  draft,
  set,
}: {
  draft: ChannelDraft
  set: <K extends keyof ChannelDraft>(key: K, value: ChannelDraft[K]) => void
}) {
  return (
    <div className="space-y-3">
      <Choose
        label="Message format"
        value={draft.dataType}
        onChange={(dataType) => {
          set('dataType', dataType)
          // MLLP is an HL7 transport and the loader refuses it on an X12 channel, so
          // switching format moves the source rather than leaving a combination that
          // cannot load. Being helpful here rather than reporting an error later.
          if (dataType === 'x12' && draft.sourceKind === 'mllp') set('sourceKind', 'http')
          if (dataType === 'edifact' && draft.sourceKind === 'mllp') set('sourceKind', 'http')
          // The same reasoning for v3: MLLP answers with a v2 acknowledgement, which a v3
          // sender cannot read, so the loader refuses the combination.
          if (dataType === 'hl7v3' && draft.sourceKind === 'mllp') set('sourceKind', 'http')
          // Neither pharmacy format travels over MLLP. A claim already uses the bytes MLLP frames with, and a
          // prescription is an XML document that control characters make ill-formed, so the loader refuses both.
          if ((dataType === 'ncpdp' || dataType === 'script') && draft.sourceKind === 'mllp')
            set('sourceKind', 'http')
          // A delimited document has no MLLP framing and arrives as a file drop far more often than anything
          // else, so file is the useful default rather than the one that will be refused.
          if (dataType === 'delimited' && draft.sourceKind === 'mllp') set('sourceKind', 'file')
        }}
        options={[
          { value: 'hl7', label: 'HL7 v2 — the usual choice' },
          { value: 'x12', label: 'X12 — claims, remittances, eligibility' },
          { value: 'edifact', label: 'UN/EDIFACT — orders, invoices, European claims' },
          { value: 'hl7v3', label: 'HL7 v3 — IHE PIX and PDQ' },
          { value: 'ncpdp', label: 'NCPDP — pharmacy claims' },
          { value: 'script', label: 'NCPDP SCRIPT — prescriptions' },
          { value: 'delimited', label: 'Delimited — CSV, tab or pipe separated' },
          { value: 'dicom', label: 'DICOM — imaging' },
          { value: 'raw', label: 'Raw — delivered whole and unparsed' },
        ]}
      />

      {(draft.dataType === 'ncpdp' || draft.dataType === 'script') && (
        <div className="rounded-lg border border-teal-800/60 bg-teal-950/20 p-3 text-xs leading-relaxed text-teal-200">
          {draft.dataType === 'ncpdp' ? (
            <>
              <p className="font-medium">Pharmacy claims are checked but never changed.</p>
              <p className="mt-1.5 text-teal-300/90">
                Every transmission is parsed on the way through, so a claim whose fixed-width header is short stops
                here rather than at the switch — that failure shifts every field one place left while leaving each one
                the right length, so nothing downstream would look wrong. Nothing is corrected: the separators are part
                of the standard, so field rules, filters and contracts are refused on this format rather than quietly
                having no effect.
              </p>
            </>
          ) : (
            <>
              <p className="font-medium">Prescriptions are checked but never rewritten.</p>
              <p className="mt-1.5 text-teal-300/90">
                Each message is parsed so a truncated prescription stops here instead of reaching a pharmacy as a
                fragment. Nothing is edited in transit — this server did not write the prescription, and silently
                changing a clinical document is worse than passing on one somebody can see. Schedule II refill limits
                and the substitution flag are checked when Perfuse authors a prescription, not when it forwards one.
              </p>
            </>
          )}
        </div>
      )}

      {draft.dataType === 'hl7v3' && (
        <div className="space-y-3 rounded-lg border border-slate-800 p-3">
          <Text
            label="Filter (v3 paths)"
            value={draft.v3Filter}
            onChange={(v) => set('v3Filter', v)}
            placeholder={'//administrativeGenderCode@code == "F"'}
            hint={
              'Leave empty to forward everything. v3 keeps most values in attributes, so a path usually ' +
              'ends in @value or @code. The field picker on the Playground tab will produce one from ' +
              'a message your sender actually sent.'
            }
          />

          {/* Said here rather than left to a load error, because it is the mistake somebody makes on
              their first v3 channel: they copy a working v2 one and the filter never matches. */}
          <p className="rounded border border-amber-700 bg-amber-950/40 px-2 py-1.5 text-xs leading-relaxed text-amber-200">
            This is a different filter from the one on the Filter tab. That one reads HL7 v2 segment
            paths like <code className="font-mono">PID-8</code> and would never match a v3 message, so
            a v3 channel uses this field instead. The same applies to transformations: a v3 channel has
            its own, below, and the ones on the Transform tab address v2 fields. Scripts do work, and
            a v3 document reaches them as itself — a value in an attribute is read and written with{' '}
            <code className="font-mono">{"['@value']"}</code>, since in v3 that is where a value
            usually lives.
          </p>

          <Check
            label="Send an acknowledgement back"
            value={draft.v3Acknowledge}
            onChange={(v) => set('v3Acknowledge', v)}
            hint={
              'A v3 sender is usually waiting on one, and a sender that receives nothing normally ' +
              'retries — which is how a patient gets registered three times. Turn this off only for a ' +
              'feed that genuinely expects no reply.'
            }
          />

          {draft.v3Acknowledge && (
            <Pair>
              <Text
                label="Our device name"
                value={draft.v3SenderDevice}
                onChange={(v) => set('v3SenderDevice', v)}
                placeholder="PERFUSE"
                hint="Goes in the acknowledgement. A receiver that does not recognise it may discard the reply, which looks exactly like sending nothing."
              />
              <Text
                label="Our root OID"
                value={draft.v3SenderOid}
                onChange={(v) => set('v3SenderOid', v)}
                placeholder="2.16.840.1.113883.3.999"
                hint="The numbering scheme our device name belongs to. Your organisation will have one assigned."
              />
            </Pair>
          )}

          {draft.v3Acknowledge && (!draft.v3SenderDevice || !draft.v3SenderOid) && (
            // Warned inline rather than only at save, because this is a configuration somebody would
            // otherwise build, save, and have refused.
            <p className="rounded border border-rose-700 bg-rose-950/40 px-2 py-1.5 text-xs text-rose-200">
              Both fields are required while acknowledgement is on. The server will refuse this channel
              otherwise.
            </p>
          )}

          {draft.sourceKind !== 'http' && draft.sourceKind !== 'soap' && draft.v3Acknowledge && (
            <p className="rounded border border-rose-700 bg-rose-950/40 px-2 py-1.5 text-xs leading-relaxed text-rose-200">
              A v3 acknowledgement goes back in the response body, so it needs an HTTP or SOAP source.
              This source has no open connection to reply on — turn acknowledgement off, or change the
              source.
            </p>
          )}
        </div>
      )}

      {draft.dataType === 'delimited' && (
        <div className="space-y-3 rounded-lg border border-slate-800 p-3">
          {/* A delimited channel could be chosen and then not configured at all.
              
              Every one of these was settable in the file and absent from the interface, which for a whole format meant the format was
              choosable and not usable without a text editor. */}
          <Pair>
            <Text
              label="Column separator"
              value={draft.delimitedDelimiter}
              onChange={(v) => set('delimitedDelimiter', v)}
              placeholder=","
              mono
              hint="One character. Write a tab as \t. Empty means a comma."
            />
            <Text
              label="Quote character"
              value={draft.delimitedQuote}
              onChange={(v) => set('delimitedQuote', v)}
              placeholder='"'
              mono
              hint={'Empty means a double quote. Set it to nothing meaningful only if the file genuinely has no quoting, because a quoted separator then splits a field in two.'}
            />
          </Pair>

          <Check
            label="The first row names the columns"
            value={draft.delimitedHasHeader}
            onChange={(v) => set('delimitedHasHeader', v)}
            hint="With this on, columns are addressed by the names in the first row and that row is not treated as data. With it off, the names below are used instead — and if neither is set, columns can only be addressed by position."
          />

          {!draft.delimitedHasHeader && (
            <Text
              label="Column names, in order"
              value={draft.delimitedColumns}
              onChange={(v) => set('delimitedColumns', v)}
              placeholder="mrn, surname, given, dob"
              mono
              hint="Comma separated. Needed when the file has no header row, because a filter written against a name has nothing to match otherwise."
            />
          )}

          <Check
            label="Trim spaces around each value"
            value={draft.delimitedTrimSpace}
            onChange={(v) => set('delimitedTrimSpace', v)}
            hint="Fixes the common case of a value padded to a column width. Leave it off if trailing spaces are significant, which they occasionally are in an identifier."
          />

          <Check
            label="Keep blank lines as rows"
            value={draft.delimitedKeepBlankLines}
            onChange={(v) => set('delimitedKeepBlankLines', v)}
            hint="Off by default, and usually right. On, a blank line becomes a row of empty columns — which matters only if a count has to match the file exactly."
          />

          <Check
            label="Accept rows with the wrong number of columns"
            value={draft.delimitedRelaxed}
            onChange={(v) => set('delimitedRelaxed', v)}
            hint="Off means a row with too many or too few columns is an error, which is what catches a file that changed shape without warning. On, it is accepted and the missing columns are empty."
          />

          <Text
            label="Lines starting with this are ignored"
            value={draft.delimitedComment}
            onChange={(v) => set('delimitedComment', v)}
            placeholder="#"
            mono
            hint="One character. Empty means no line is treated as a comment."
          />
        </div>
      )}

      {draft.dataType === 'edifact' && (
        <div className="space-y-3 rounded-lg border border-slate-800 p-3" data-testid="edifact-settings">
          <Choose
            label="Envelope checking"
            value={draft.edifactEnvelope}
            onChange={(v) => set('edifactEnvelope', v)}
            options={[
              { value: 'require', label: 'reject an interchange whose UNT, UNE or UNZ does not match' },
              { value: 'warn', label: 'accept it, but record a warning' },
              { value: 'ignore', label: 'do not check' },
            ]}
            hint="An interchange states its own segment and message counts and repeats its references in the trailers, which is what makes a transfer cut short detectable."
          />
          <Check
            label="Split each interchange into one per message"
            value={draft.edifactSplit}
            onChange={(v) => set('edifactSplit', v)}
            hint="Each order or invoice becomes its own interchange with the original UNB, so it can be searched, filtered and retried on its own."
          />
          <Choose
            label="Answer the sender with"
            value={draft.edifactAcknowledge}
            onChange={(v) => set('edifactAcknowledge', v)}
            options={[
              { value: 'none', label: 'nothing' },
              { value: 'contrl', label: 'a CONTRL in the HTTP response: accepted (7) or rejected (4)' },
            ]}
            hint="For a sender posting over HTTP, which gets it in the response. Over AS2 the partner's receipt is the acknowledgement."
          />
          {draft.edifactSplit && draft.edifactAcknowledge === 'contrl' && (
            <p className="rounded border border-amber-800/50 bg-amber-950/20 p-2 text-xs leading-relaxed text-amber-200/90">
              Splitting and acknowledging cannot both be on: a CONTRL answers a whole interchange. Turn one of the two off.
            </p>
          )}
          <p className="text-xs leading-relaxed text-slate-400">
            The filter addresses elements by segment and position: <code className="font-mono">BGM-2</code>,{' '}
            <code className="font-mono">NAD(2)-2.1</code>, <code className="font-mono">UNB-2.1</code> for the sender. The interchange is
            delivered as the sender wrote it.
          </p>
        </div>
      )}

      {draft.dataType === 'x12' && (
        <div className="space-y-3 rounded-lg border border-slate-800 p-3">
          <Choose
            label="Envelope checking"
            value={draft.x12Envelope}
            onChange={(v) => set('x12Envelope', v)}
            options={[
              { value: 'require', label: 'reject an interchange whose counts do not match' },
              { value: 'warn', label: 'accept it, but record a warning' },
              { value: 'ignore', label: 'do not check' },
            ]}
            hint="X12 states its own segment and transaction counts, which is what makes a truncated transfer detectable rather than silent. Rejecting is the right default; the other two exist because some partners have shipped wrong counts for years."
          />
          <Check
            label="Split each interchange into one message per transaction set"
            value={draft.x12Split}
            onChange={(v) => set('x12Split', v)}
            hint="Usually wanted. A file of two hundred claims becomes two hundred messages you can search, filter and retry one at a time, rather than one all-or-nothing blob."
          />

          <Choose
            label="Answer the trading partner with"
            value={draft.x12Acknowledge}
            onChange={(v: string) => set('x12Acknowledge', v as ChannelDraft['x12Acknowledge'])}
            options={[
              { value: 'none', label: 'nothing — they expect a reply later, as its own file' },
              { value: '999', label: 'a 999 in the response — for HIPAA and real-time' },
              { value: '997', label: 'a 997 in the response — for older payer connections' },
              { value: 'ta1', label: 'a TA1 in the response — envelope only' },
            ]}
            hint="Which one is a property of your agreement with the partner, not of the message. A 999 supersedes a 997 for HIPAA transactions, but plenty of older payer connections expect a 997 and will treat a 999 as a file they do not recognise. Sending the wrong one is worse than sending nothing, because it answers a question nobody asked."
          />

          {draft.x12Acknowledge !== 'none' && (
            <div className="space-y-3 rounded-lg border border-slate-700 bg-slate-950/40 p-3">
              <p className="text-xs leading-relaxed text-slate-400">
                An acknowledgement has to say who it is from. These are the values the partner has
                configured for you — an interchange whose sender they do not recognise is discarded
                before anybody reads it, which looks exactly like sending nothing at all.
              </p>
              <Pair>
                <Text
                  label="Our identifier"
                  required
                  value={draft.x12AckSenderId}
                  onChange={(v: string) => set('x12AckSenderId', v)}
                  placeholder="PERFUSE"
                  mono
                  hint="Goes in ISA06. Ask the partner what they have on file for you."
                />
                <Choose
                  label="What kind of identifier"
                  value={draft.x12AckSenderQualifier}
                  onChange={(v: string) => set('x12AckSenderQualifier', v)}
                  options={[
                    { value: 'ZZ', label: 'ZZ — mutually agreed (the usual answer)' },
                    { value: '01', label: '01 — Duns number' },
                    { value: '14', label: '14 — Duns plus suffix' },
                    { value: '20', label: '20 — health industry number' },
                    { value: '27', label: '27 — carrier identification' },
                    { value: '30', label: '30 — US tax identification number' },
                    { value: '33', label: '33 — NAIC company code' },
                  ]}
                  hint="Goes in ISA05."
                />
              </Pair>
              {draft.x12Split && (
                <p className="rounded border border-amber-800/50 bg-amber-950/20 p-2 text-xs leading-relaxed text-amber-200/90">
                  Splitting and acknowledging cannot both be on. An acknowledgement is a statement
                  about a whole interchange, so splitting would send one per transaction set and the
                  partner would have no way to reconcile them. Turn one of the two off.
                </p>
              )}
              {draft.sourceKind !== 'http' &&
                draft.sourceKind !== 'soap' &&
                draft.sourceKind !== 'mllp' && (
                  <p className="rounded border border-amber-800/50 bg-amber-950/20 p-2 text-xs leading-relaxed text-amber-200/90">
                    A {draft.sourceKind} source has no open connection to answer on, so this
                    acknowledgement can never be sent. Real-time acknowledgement needs the sender to
                    be waiting — HTTP, SOAP or MLLP. A batch file needs its acknowledgement returned
                    later as its own interchange, which Perfuse cannot do yet.
                  </p>
                )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

/** The region, keys and endpoint an SQS or S3 source signs with. */
function AwsSourceAccess({
  draft,
  set,
}: {
  draft: ChannelDraft
  set: <K extends keyof ChannelDraft>(key: K, value: ChannelDraft[K]) => void
}) {
  return (
    <>
      <Pair>
        <Text
          label="Region"
          required
          value={draft.awsSrcRegion}
          onChange={(v) => set('awsSrcRegion', v)}
          placeholder="eu-west-2"
          mono
          hint="Part of the request signature, so a wrong one fails as if the credentials were wrong."
        />
        <Text
          label="Endpoint"
          value={draft.awsSrcEndpoint}
          onChange={(v) => set('awsSrcEndpoint', v)}
          placeholder="Leave empty for AWS"
          mono
          hint="Only for LocalStack, MinIO, a VPC endpoint and the like."
        />
      </Pair>
      <Pair>
        <Text
          label="Access key ID"
          required
          value={draft.awsSrcAccessKeyId}
          onChange={(v) => set('awsSrcAccessKeyId', v)}
          placeholder="${AWS_ACCESS_KEY_ID}"
          mono
          hint="Reference the environment rather than writing the value, because this file goes into version control."
        />
        <Secret
          label="Secret access key"
          value={draft.awsSrcSecretAccessKey}
          onChange={(v) => set('awsSrcSecretAccessKey', v)}
          hint="Write ${AWS_SECRET_ACCESS_KEY} to read it from the environment at startup."
        />
      </Pair>
      <Secret
        label="Session token"
        value={draft.awsSrcSessionToken}
        onChange={(v) => set('awsSrcSessionToken', v)}
        hint="Write ${AWS_SESSION_TOKEN} to read it from the environment. Only for temporary credentials, which expire - and the channel stops reading when they do."
      />
    </>
  )
}
