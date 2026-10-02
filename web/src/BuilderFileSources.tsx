import { Advanced, Check, Choose, Num, Pair, Secret, Text } from './builderFields'
import type { ChannelDraft } from './model'
import { useState } from 'react'

/**
 * More is a self-managing Advanced section.
 *
 * The shared Advanced control is deliberately controlled by its parent, which is right where several sections open and
 * close together. Here each group is independent, so holding the state locally avoids threading six booleans through
 * every one of these components.
 */
function More({ title, children }: { title: string; children: React.ReactNode }) {
  const [open, setOpen] = useState(false)
  return (
    <Advanced title={title} open={open} onToggle={() => setOpen(!open)}>
      {children}
    </Advanced>
  )
}

// The connectors that read files and raw streams.
//
// Kept out of BuilderSourceSection so that file lists source kinds and delegates, rather than growing a sixth screen of
// conditional fields.
//
// The hints carry what actually goes wrong, because that is what somebody is paying a consultant for. A pattern of * reads
// the temporary file a sending system is still writing. A settle time of zero delivers half a message that parses. An
// archive inside the source directory delivers everything again on the next poll, forever. None of that is discoverable
// from a field label.

type Setter = <K extends keyof ChannelDraft>(key: K, value: ChannelDraft[K]) => void

/**
 * FileCollectionFields is everything about consuming a directory that does not depend on the transport.
 *
 * Shared by the local, FTP, SMB and WebDAV sources, matching the server where the settle rule and disposal live in one
 * poller. Four copies of this form would drift, and the differences would only appear as a site whose files behaved oddly
 * on one transport.
 */
export function FileCollectionFields({ draft, set }: { draft: ChannelDraft; set: Setter }) {
  return (
    <>
      <Pair>
        <Text
          label="Directory to read"
          value={draft.fileDir}
          onChange={(v) => set('fileDir', v)}
          placeholder="."
          mono
          hint="Relative to the root above. A single dot means the root itself."
        />
        <Text
          label="Only files matching"
          required
          value={draft.filePattern}
          onChange={(v) => set('filePattern', v)}
          placeholder="*.hl7"
          mono
          hint="A pattern of * reads everything, including a temporary file a sending system is still writing under its final name."
        />
      </Pair>

      <Pair>
        <Num
          label="Check every (seconds)"
          value={draft.filePollSeconds}
          onChange={(v) => set('filePollSeconds', v ?? 30)}
        />
        <Num
          label="Wait for the file to stop changing (seconds)"
          value={draft.fileStableSeconds}
          onChange={(v) => set('fileStableSeconds', v ?? 5)}
          hint="The most important setting here. Read too early, half an HL7 message usually still parses, so it is accepted, acknowledged and delivered with nothing to say the rest was missing."
        />
      </Pair>

      <Choose
        label="Once the messages are accepted"
        value={draft.fileAfterRead}
        onChange={(v) => set('fileAfterRead', v)}
        options={[
          { value: 'move', label: 'Move the file to an archive directory' },
          { value: 'delete', label: 'Delete the file' },
          { value: 'leave', label: 'Leave it where it is' },
        ]}
        hint="Leaving it means nothing about the file changes, so only a list held in memory stops every message being processed again after a restart."
      />

      <Pair>
        {draft.fileAfterRead === 'move' && (
          <Text
            label="Archive directory"
            required
            value={draft.fileMoveTo}
            onChange={(v) => set('fileMoveTo', v)}
            placeholder="processed"
            mono
            hint="Must not be the directory being read. A channel that archives into its own source reads it again on the next poll and delivers every message a second time, then a third."
          />
        )}
        <Text
          label="Where failures go"
          value={draft.fileErrorDir}
          onChange={(v) => set('fileErrorDir', v)}
          placeholder="errors"
          mono
          hint="Kept separate from the archive on purpose: a directory holding nothing but failures is one somebody can watch."
        />
      </Pair>

      <More title="Batches, ordering and file contents">
        <Pair>
          <Num
            label="Files per check"
            value={draft.fileBatchSize}
            onChange={(v) => set('fileBatchSize', v ?? 0)}
            hint="Zero means no limit. After an outage that can be tens of thousands of files in one pass, during which the channel looks hung."
          />
          <Choose
            label="Read them in order of"
            value={draft.fileSortBy}
            onChange={(v) => set('fileSortBy', v)}
            options={[
              { value: 'name', label: 'Name' },
              { value: 'modified', label: 'Modification time' },
              { value: 'none', label: 'No particular order' },
            ]}
            hint="Not cosmetic: an A08 update read before its A01 admission produces a patient who does not exist yet. Directory order is arbitrary."
          />
        </Pair>

        <Check
          label="Each file is one message, delivered unparsed"
          value={draft.fileRaw}
          onChange={(v) => set('fileRaw', v)}
          hint="For a PDF, a zip, a CSV batch or a proprietary export. Needs the channel data type set to raw as well, and the two are checked against each other."
        />
        <Check
          label="Files contain MLLP framing"
          value={draft.fileFramed}
          onChange={(v) => set('fileFramed', v)}
          hint="Off by default, which splits the file at each MSH segment. Only turn this on if the file really has the framing bytes in it."
        />
      </More>
    </>
  )
}

/**
 * FramingFields is how message boundaries are found on a raw socket or a serial cable.
 *
 * Shared by both because the framings are the same wherever the bytes come from: an analyser that speaks STX/ETX over a
 * socket speaks it over a cable.
 */
export function FramingFields({ draft, set }: { draft: ChannelDraft; set: Setter }) {
  return (
    <>
      <Choose
        label="How messages are separated"
        required
        value={draft.framingMode}
        onChange={(v) => set('framingMode', v)}
        options={[
          { value: '', label: 'Choose one — the device manual states it' },
          { value: 'delimited', label: 'A character marks the end (most devices)' },
          { value: 'mllp', label: 'MLLP framing, as HL7 uses over TCP' },
          { value: 'fixed', label: 'Every message is the same number of bytes' },
          { value: 'length', label: 'A length is sent before each message' },
          { value: 'whole', label: 'One message per connection, ending when it closes' },
        ]}
        hint="There is no default, deliberately. The wrong choice does not fail: it delivers messages cut in half or joined together, both of which usually still parse."
      />

      {draft.framingMode === 'delimited' && (
        <Pair>
          <Text
            label="End of message"
            required
            value={draft.framingDelimiter}
            onChange={(v) => set('framingDelimiter', v)}
            placeholder="\r"
            mono
            hint="Write control characters as escapes: \r for carriage return, \x03 for ETX, \n for newline."
          />
          <Text
            label="Start of message, if there is one"
            value={draft.framingStartBlock}
            onChange={(v) => set('framingStartBlock', v)}
            placeholder="\x02"
            mono
            hint="Anything arriving before it is discarded as noise from a partial connection. \x02 is STX."
          />
        </Pair>
      )}

      {draft.framingMode === 'delimited' && (
        <Check
          label="Keep the delimiter in the message"
          value={draft.framingKeepDelimiter}
          onChange={(v) => set('framingKeepDelimiter', v)}
          hint="Off by default, and usually right: the delimiter is framing rather than content, so keeping it puts a stray control character at the end of every message and into anything derived from it."
        />
      )}

      {draft.framingMode === 'fixed' && (
        <Num
          label="Bytes per message"
          required
          value={draft.framingRecordLength}
          onChange={(v) => set('framingRecordLength', v ?? 0)}
          hint="Unforgiving: one byte out of step and every message after it is misaligned, and neither end can detect that. Prefer a delimiter or a length if the device offers one."
        />
      )}

      {draft.framingMode === 'fixed' && (
        <Check
          label="Trim padding from each record"
          value={draft.framingTrimPadding}
          onChange={(v) => set('framingTrimPadding', v)}
          hint="On by default. A fixed-length record is padded to its width, so without this every value carries trailing spaces - which then travel into comparisons, identifiers and anything derived from them."
        />
      )}

      {draft.framingMode === 'length' && (
        <>
          <Pair>
            <Choose
              label="Length header size"
              value={String(draft.framingLengthBytes) as '1' | '2' | '4' | '8'}
              onChange={(v) => set('framingLengthBytes', Number(v))}
              options={[
                { value: '1', label: '1 byte' },
                { value: '2', label: '2 bytes' },
                { value: '4', label: '4 bytes' },
                { value: '8', label: '8 bytes' },
              ]}
            />
            <Check
              label="Most significant byte first"
              value={draft.framingBigEndian}
              onChange={(v) => set('framingBigEndian', v)}
              hint="Also called big-endian or network order. Wrong, and the advertised length is wildly out — usually so large the connection is dropped."
            />
          </Pair>
          <Check
            label="The length includes the header itself"
            value={draft.framingLengthIncludesHeader}
            onChange={(v) => set('framingLengthIncludesHeader', v)}
            hint="Both conventions exist and the difference is silent: with a four-byte header the payload is wrong by four bytes every time, which for text usually still parses."
          />
        </>
      )}

      <More title="Replying to the device">
        <Choose
          label="After each message, send back"
          value={draft.sourceReply}
          onChange={(v) => set('sourceReply', v)}
          options={[
            { value: 'none', label: 'Nothing (what most devices expect)' },
            { value: 'ack', label: 'A single ACK byte (0x06)' },
            { value: 'text', label: 'A fixed string' },
          ]}
          hint="A device that expects a reply and does not get one usually retries the same message forever, which looks like a duplicate storm rather than a missing reply."
        />
        {draft.sourceReply === 'text' && (
          <Text
            label="What to send"
            required
            value={draft.sourceReplyText}
            onChange={(v) => set('sourceReplyText', v)}
            mono
            hint="Escapes work here too."
          />
        )}
      </More>
    </>
  )
}

/** LocalFileFields is a directory on this server. */
export function LocalFileFields({ draft, set }: { draft: ChannelDraft; set: Setter }) {
  return (
    <>
      <Text
        label="Directory this channel may read"
        required
        value={draft.fileRoot}
        onChange={(v) => set('fileRoot', v)}
        placeholder="/var/spool/lab-results"
        mono
        hint="Everything below is relative to this, and anything resolving outside it is refused. It is not created automatically: a typo would otherwise poll an empty directory forever and report nothing wrong."
      />
      <FileCollectionFields draft={draft} set={set} />
      <More title="Links">
        <Check
          label="Follow links that point outside the directory"
          value={draft.fileFollowSymlinks}
          onChange={(v) => set('fileFollowSymlinks', v)}
          hint="Off by default. For a directory that other systems write into, a link out of the tree is usually what it looks like."
        />
      </More>
    </>
  )
}

/** FTPSourceFields collects files from an FTP or FTPS server. */
export function FTPSourceFields({ draft, set }: { draft: ChannelDraft; set: Setter }) {
  return (
    <>
      <Pair>
        <Text
          label="Server"
          required
          value={draft.ftpSrcHost}
          onChange={(v) => set('ftpSrcHost', v)}
          placeholder="ftp.example.org:21"
          mono
        />
        <Choose
          label="Encryption"
          value={draft.ftpSrcSecurity}
          onChange={(v) => set('ftpSrcSecurity', v)}
          options={[
            { value: 'explicit', label: 'FTPS, negotiated on the normal port (usual)' },
            { value: 'implicit', label: 'FTPS on a dedicated port' },
            { value: 'none', label: 'Plain FTP — password sent in clear text' },
          ]}
          hint="Most surviving FTP servers accept explicit FTPS, which is the same protocol with TLS and needs no change at the far end."
        />
      </Pair>
      <Pair>
        <Text label="Username" value={draft.ftpSrcUser} onChange={(v) => set('ftpSrcUser', v)} />
        <Secret
          label="Password"
          value={draft.ftpSrcPassword}
          onChange={(v) => set('ftpSrcPassword', v)}
        />
      </Pair>
      <Text
        label="Directory this channel may read"
        required
        value={draft.fileRoot}
        onChange={(v) => set('fileRoot', v)}
        placeholder="/incoming"
        mono
        hint="The server has no notion of this, so it is the only thing confining the channel: an FTP server will happily accept a path containing .. and write wherever the account can reach."
      />
      <FileCollectionFields draft={draft} set={set} />
    </>
  )
}

/** SMBSourceFields collects files from a Windows file share. */
export function SMBSourceFields({ draft, set }: { draft: ChannelDraft; set: Setter }) {
  return (
    <>
      <Pair>
        <Text
          label="Server"
          required
          value={draft.smbHost}
          onChange={(v) => set('smbHost', v)}
          placeholder="fileserver.hospital.local"
          mono
        />
        <Text
          label="Share name"
          required
          value={draft.smbShare}
          onChange={(v) => set('smbShare', v)}
          placeholder="data"
          mono
          hint="The share name only — the data in \\\\server\\data. A path here is refused rather than trimmed; put the folder below."
        />
      </Pair>
      <Pair>
        <Text label="Username" required value={draft.smbUser} onChange={(v) => set('smbUser', v)} />
        <Secret label="Password" value={draft.smbPassword} onChange={(v) => set('smbPassword', v)} />
      </Pair>
      <Pair>
        <Text
          label="Domain"
          value={draft.smbDomain}
          onChange={(v) => set('smbDomain', v)}
          hint="Leave empty for a local account on the server. A hospital domain account needs it."
        />
        <Text
          label="Folder within the share"
          value={draft.fileRoot}
          onChange={(v) => set('fileRoot', v)}
          placeholder="hl7/inbound"
          mono
        />
      </Pair>
      <FileCollectionFields draft={draft} set={set} />
    </>
  )
}

/** WebDAVSourceFields collects files from a WebDAV collection. */
export function WebDAVSourceFields({ draft, set }: { draft: ChannelDraft; set: Setter }) {
  return (
    <>
      <Text
        label="Collection address"
        required
        value={draft.webdavUrl}
        onChange={(v) => set('webdavUrl', v)}
        placeholder="https://docs.example.org/remote.php/dav/files/perfuse/inbox"
        mono
        hint="Use https where the server offers it. WebDAV authentication is usually basic, which sends the password itself rather than a hash."
      />
      <Pair>
        <Text label="Username" value={draft.webdavUser} onChange={(v) => set('webdavUser', v)} />
        <Secret
          label="Password"
          value={draft.webdavPassword}
          onChange={(v) => set('webdavPassword', v)}
        />
      </Pair>
      <FileCollectionFields draft={draft} set={set} />
    </>
  )
}

/** TCPSourceFields listens on a raw socket. */
export function TCPSourceFields({ draft, set }: { draft: ChannelDraft; set: Setter }) {
  return (
    <>
      <Text
        label="Listen on"
        required
        value={draft.tcpListen}
        onChange={(v) => set('tcpListen', v)}
        placeholder="0.0.0.0:6000"
        mono
        hint="0.0.0.0 accepts from anywhere on the network. A device on the same subnet cannot reach a listener bound to 127.0.0.1."
      />
      <FramingFields draft={draft} set={set} />
    </>
  )
}

/** SerialSourceFields reads a device over a cable. */
export function SerialSourceFields({ draft, set }: { draft: ChannelDraft; set: Setter }) {
  return (
    <>
      <Pair>
        <Text
          label="Port"
          required
          value={draft.serialPort}
          onChange={(v) => set('serialPort', v)}
          placeholder="/dev/ttyUSB0"
          mono
          hint="On Windows this is COM3 or similar. On macOS it usually starts /dev/tty.usbserial."
        />
        <Num
          label="Speed (baud)"
          required
          value={draft.serialBaud}
          onChange={(v) => set('serialBaud', v ?? 0)}
          hint="No default, deliberately. A wrong speed does not fail: it delivers readable-looking nonsense. The device manual states it."
        />
      </Pair>

      <Pair>
        <Choose
          label="Data bits"
          value={String(draft.serialDataBits) as '5' | '6' | '7' | '8'}
          onChange={(v) => set('serialDataBits', Number(v))}
          options={[
            { value: '8', label: '8 (usual)' },
            { value: '7', label: '7' },
            { value: '6', label: '6' },
            { value: '5', label: '5' },
          ]}
        />
        <Choose
          label="Parity"
          value={draft.serialParity}
          onChange={(v) => set('serialParity', v)}
          options={[
            { value: 'none', label: 'None' },
            { value: 'even', label: 'Even' },
            { value: 'odd', label: 'Odd' },
            { value: 'mark', label: 'Mark' },
            { value: 'space', label: 'Space' },
          ]}
          hint="Seven data bits almost always goes with even or odd parity. Seven with none usually means this was missed."
        />
      </Pair>

      <Pair>
        <Choose
          label="Stop bits"
          value={draft.serialStopBits}
          onChange={(v) => set('serialStopBits', v)}
          options={[
            { value: '1', label: '1 (usual)' },
            { value: '1.5', label: '1.5' },
            { value: '2', label: '2' },
          ]}
        />
        <Choose
          label="Flow control"
          value={draft.serialFlowControl}
          onChange={(v) => set('serialFlowControl', v)}
          options={[
            { value: 'none', label: 'None' },
            { value: 'hardware', label: 'Hardware (RTS/CTS)' },
            { value: 'software', label: 'Software (XON/XOFF)' },
          ]}
          hint="If the device expects hardware flow control and does not get it, it stops partway through a long message — the result is a truncated message rather than an error."
        />
      </Pair>

      <Num
        label="Warn if silent for (seconds)"
        value={draft.serialQuietSeconds}
        onChange={(v) => set('serialQuietSeconds', v ?? 0)}
        hint="The only way anybody learns this feed has died. A cable has no connection to lose, so an unplugged cable, a device switched off and a device with nothing to say are the same silence. Set it somewhat longer than the longest gap this device normally leaves."
      />

      <FramingFields draft={draft} set={set} />
    </>
  )
}
