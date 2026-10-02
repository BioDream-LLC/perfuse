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
  { value: 'javascript', label: 'A script we write produces messages (JavaScript Reader)' },
  { value: 'file', label: 'We read files from a folder on this server' },
  { value: 'ftp', label: 'We collect files from a server (FTP or FTPS)' },
  { value: 'smb', label: 'We collect files from a Windows share' },
  { value: 'webdav', label: 'We collect files from a WebDAV collection' },
  { value: 'tcp', label: 'A device connects to us on a socket, not MLLP (TCP)' },
  { value: 'serial', label: 'A device is wired to us (serial cable)' },
  { value: 'broker', label: 'We read from a message queue (ActiveMQ, RabbitMQ)' },
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
  const [showDicomAdvanced, setShowDicomAdvanced] = useState(false)
  const [showMllpTls, setShowMllpTls] = useState(false)
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
            <Text
              label="Element holding the message"
              value={draft.soapSourceElement}
              onChange={(v) => set('soapSourceElement', v)}
              placeholder="HL7Message"
              hint="The element inside the envelope body that contains the HL7. Namespace prefixes are ignored,
              so give the local name only — callers disagree about prefixes and none of them are wrong."
            />
          </Pair>

          <Check
            label="The message arrives base64 encoded"
            value={draft.soapSourceBase64}
            onChange={(v) => set('soapSourceBase64', v)}
            hint="Common, because it stops the segment separators being mangled by XML. Turn it on if the caller
            says they encode — with it off, an encoded message arrives as one long unparseable line."
          />

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
