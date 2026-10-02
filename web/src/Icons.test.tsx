/**
 * Tests for Icons.tsx - colourful inline SVG icon set.
 *
 * Verifies correct rendering, gradient id uniqueness (collision safety),
 * accessibility attributes, and lookup map completeness.
 *
 * @vitest-environment happy-dom
 */
import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/react'
import {
  type IconProps,
  viewIcons,
  sourceIcons,
  formatIcons,
  IconDashboard,
  IconChannels,
  IconMessages,
  IconQueue,
  IconAlerts,
  IconMetrics,
  IconFhirLab,
  IconDocuments,
  IconScripts,
  IconMigrate,
  IconContracts,
  IconTables,
  IconMapper,
  IconFleet,
  IconFlowMap,
  IconPlayground,
  IconShadow,
  IconCertificates,
  IconUsers,
  IconActivity,
  IconSettings,
  IconRunning,
  IconStopped,
  IconPaused,
  IconError,
  IconWarning,
  IconSuccess,
  IconInfo,
  IconPending,
  IconStart,
  IconStop,
  IconEdit,
  IconDelete,
  IconCopy,
  IconDownload,
  IconUpload,
  IconSearch,
  IconRefresh,
  IconAdd,
  IconClose,
  IconCheck,
  IconChevronDown,
  IconChevronRight,
  IconExternal,
  IconFilter,
  IconSave,
  IconSend,
  IconTrash,
  IconEye,
  IconMllp,
  IconHttp,
  IconSftp,
  IconDatabase,
  IconSoap,
  IconDicom,
  IconJavaScript,
  IconEmail,
  IconFtp,
  IconTcp,
  IconHl7,
  IconX12,
  IconFhir,
  IconCda,
  IconJson,
  IconXml,
  IconPdf,
  IconCsv,
  IconPatient,
  IconHospital,
  IconLab,
  IconPharmacy,
  IconImaging,
  IconImmunisation,
  IconBilling,
  IconShield,
  IconLock,
  IconKey,
  IconHeartbeat,
  IconStethoscope,
} from './Icons'

const allIcons: Array<[string, React.ComponentType<IconProps>]> = [
  ['IconDashboard', IconDashboard],
  ['IconChannels', IconChannels],
  ['IconMessages', IconMessages],
  ['IconQueue', IconQueue],
  ['IconAlerts', IconAlerts],
  ['IconMetrics', IconMetrics],
  ['IconFhirLab', IconFhirLab],
  ['IconDocuments', IconDocuments],
  ['IconScripts', IconScripts],
  ['IconMigrate', IconMigrate],
  ['IconContracts', IconContracts],
  ['IconTables', IconTables],
  ['IconMapper', IconMapper],
  ['IconFleet', IconFleet],
  ['IconFlowMap', IconFlowMap],
  ['IconPlayground', IconPlayground],
  ['IconShadow', IconShadow],
  ['IconCertificates', IconCertificates],
  ['IconUsers', IconUsers],
  ['IconActivity', IconActivity],
  ['IconSettings', IconSettings],
  ['IconRunning', IconRunning],
  ['IconStopped', IconStopped],
  ['IconPaused', IconPaused],
  ['IconError', IconError],
  ['IconWarning', IconWarning],
  ['IconSuccess', IconSuccess],
  ['IconInfo', IconInfo],
  ['IconPending', IconPending],
  ['IconStart', IconStart],
  ['IconStop', IconStop],
  ['IconEdit', IconEdit],
  ['IconDelete', IconDelete],
  ['IconCopy', IconCopy],
  ['IconDownload', IconDownload],
  ['IconUpload', IconUpload],
  ['IconSearch', IconSearch],
  ['IconRefresh', IconRefresh],
  ['IconAdd', IconAdd],
  ['IconClose', IconClose],
  ['IconCheck', IconCheck],
  ['IconChevronDown', IconChevronDown],
  ['IconChevronRight', IconChevronRight],
  ['IconExternal', IconExternal],
  ['IconFilter', IconFilter],
  ['IconSave', IconSave],
  ['IconSend', IconSend],
  ['IconTrash', IconTrash],
  ['IconEye', IconEye],
  ['IconMllp', IconMllp],
  ['IconHttp', IconHttp],
  ['IconSftp', IconSftp],
  ['IconDatabase', IconDatabase],
  ['IconSoap', IconSoap],
  ['IconDicom', IconDicom],
  ['IconJavaScript', IconJavaScript],
  ['IconEmail', IconEmail],
  ['IconFtp', IconFtp],
  ['IconTcp', IconTcp],
  ['IconHl7', IconHl7],
  ['IconX12', IconX12],
  ['IconFhir', IconFhir],
  ['IconCda', IconCda],
  ['IconJson', IconJson],
  ['IconXml', IconXml],
  ['IconPdf', IconPdf],
  ['IconCsv', IconCsv],
  ['IconPatient', IconPatient],
  ['IconHospital', IconHospital],
  ['IconLab', IconLab],
  ['IconPharmacy', IconPharmacy],
  ['IconImaging', IconImaging],
  ['IconImmunisation', IconImmunisation],
  ['IconBilling', IconBilling],
  ['IconShield', IconShield],
  ['IconLock', IconLock],
  ['IconKey', IconKey],
  ['IconHeartbeat', IconHeartbeat],
  ['IconStethoscope', IconStethoscope],
]

describe('Icons render without throwing', () => {
  it.each(allIcons)('%s renders', (_name, Icon) => {
    const { container } = render(<Icon />)
    expect(container.querySelector('svg')).not.toBeNull()
  })
})

describe('Icons in lookup maps render without throwing', () => {
  it('every viewIcons entry renders', () => {
    for (const [key, Icon] of Object.entries(viewIcons)) {
      const { container } = render(<Icon />)
      expect(container.querySelector('svg'), `viewIcons['${key}'] must render an svg`).not.toBeNull()
    }
  })

  it('every sourceIcons entry renders', () => {
    for (const [key, Icon] of Object.entries(sourceIcons)) {
      const { container } = render(<Icon />)
      expect(container.querySelector('svg'), `sourceIcons['${key}'] must render an svg`).not.toBeNull()
    }
  })

  it('every formatIcons entry renders', () => {
    for (const [key, Icon] of Object.entries(formatIcons)) {
      const { container } = render(<Icon />)
      expect(container.querySelector('svg'), `formatIcons['${key}'] must render an svg`).not.toBeNull()
    }
  })
})

describe('Gradient id collision handling', () => {
  it('two instances of the same icon produce different gradient ids', () => {
    // This is the critical test: naive implementations share gradient ids globally
    const { container } = render(
      <div>
        <IconDashboard />
        <IconDashboard />
      </div>
    )

    const gradients = container.querySelectorAll('linearGradient')
    expect(gradients.length).toBeGreaterThanOrEqual(2)

    const ids = new Set<string>()
    gradients.forEach((g) => {
      const gId = g.getAttribute('id')
      expect(gId).not.toBeNull()
      ids.add(gId!)
    })

    // All gradient ids must be unique
    expect(ids.size).toBe(gradients.length)
  })

  it('each icon fill reference points to a gradient that exists in the same document', () => {
    const { container } = render(
      <div>
        <IconRunning />
        <IconRunning />
      </div>
    )

    // Collect all gradient ids present in the document
    const gradientIds = new Set<string>()
    container.querySelectorAll('linearGradient').forEach((g) => {
      const gId = g.getAttribute('id')
      if (gId) gradientIds.add(gId)
    })

    // Check every element that references a gradient via fill="url(#...)"
    const allElements = container.querySelectorAll('[fill]')
    allElements.forEach((el) => {
      const fill = el.getAttribute('fill')
      if (fill && fill.startsWith('url(#')) {
        const refId = fill.slice(5, -1) // extract id from url(#id)
        expect(
          gradientIds.has(refId),
          `fill references gradient "${refId}" which must exist in the document`
        ).toBe(true)
      }
    })

    // Also check stroke references
    const allStroked = container.querySelectorAll('[stroke]')
    allStroked.forEach((el) => {
      const stroke = el.getAttribute('stroke')
      if (stroke && stroke.startsWith('url(#')) {
        const refId = stroke.slice(5, -1)
        expect(
          gradientIds.has(refId),
          `stroke references gradient "${refId}" which must exist in the document`
        ).toBe(true)
      }
    })
  })

  it('gradient ids differ across different icon types rendered together', () => {
    const { container } = render(
      <div>
        <IconDashboard />
        <IconChannels />
        <IconRunning />
      </div>
    )

    const gradients = container.querySelectorAll('linearGradient')
    const ids = new Set<string>()
    gradients.forEach((g) => {
      const gId = g.getAttribute('id')
      expect(gId).not.toBeNull()
      ids.add(gId!)
    })

    expect(ids.size).toBe(gradients.length)
  })
})

describe('Accessibility', () => {
  it('icon without title has aria-hidden="true" and is absent from accessibility tree', () => {
    const { container } = render(<IconDashboard />)
    const svg = container.querySelector('svg')!
    expect(svg.getAttribute('aria-hidden')).toBe('true')
    expect(svg.getAttribute('focusable')).toBe('false')
    expect(svg.querySelector('title')).toBeNull()
    expect(svg.getAttribute('role')).toBeNull()
  })

  it('icon with title has role="img" and accessible name equal to the title', () => {
    const { container } = render(<IconDashboard title="Dashboard" />)
    const svg = container.querySelector('svg')!
    expect(svg.getAttribute('role')).toBe('img')
    expect(svg.getAttribute('aria-hidden')).toBeNull()
    const titleEl = svg.querySelector('title')
    expect(titleEl).not.toBeNull()
    expect(titleEl!.textContent).toBe('Dashboard')
  })

  it('every icon respects the title/no-title contract', () => {
    for (const [name, Icon] of allIcons) {
      // Without title
      const { container: c1 } = render(<Icon />)
      const svg1 = c1.querySelector('svg')!
      expect(svg1.getAttribute('aria-hidden'), `${name} without title should be aria-hidden`).toBe('true')

      // With title
      const { container: c2 } = render(<Icon title="Test" />)
      const svg2 = c2.querySelector('svg')!
      expect(svg2.getAttribute('role'), `${name} with title should have role=img`).toBe('img')
      expect(svg2.querySelector('title')?.textContent, `${name} title text`).toBe('Test')
    }
  })
})

describe('Lookup map completeness', () => {
  const expectedViewKeys = [
    'Dashboard', 'Channels', 'Messages', 'Queue', 'Alerts', 'Metrics',
    'FHIR lab', 'Documents', 'Scripts', 'Migrate', 'Contracts', 'Tables',
    'AI Mapper', 'Fleet', 'Flow map', 'Playground', 'Shadow', 'Certificates',
    'Users', 'Activity', 'Settings',
  ]

  const expectedSourceKeys = [
    'mllp', 'http', 'sftp', 'database', 'soap', 'dicom',
    'dicom_query', 'dicom_move', 'dicom_worklist', 'dicomweb', 'javascript',
  ]

  const expectedFormatKeys = ['hl7', 'x12', 'hl7v3', 'fhir', 'cda']

  it('viewIcons has all expected keys and no undefined values', () => {
    for (const key of expectedViewKeys) {
      expect(viewIcons[key], `viewIcons['${key}'] must be defined`).toBeDefined()
      expect(typeof viewIcons[key], `viewIcons['${key}'] must be a function`).toBe('function')
    }
  })

  it('sourceIcons has all expected keys and no undefined values', () => {
    for (const key of expectedSourceKeys) {
      expect(sourceIcons[key], `sourceIcons['${key}'] must be defined`).toBeDefined()
      expect(typeof sourceIcons[key], `sourceIcons['${key}'] must be a function`).toBe('function')
    }
  })

  it('formatIcons has all expected keys and no undefined values', () => {
    for (const key of expectedFormatKeys) {
      expect(formatIcons[key], `formatIcons['${key}'] must be defined`).toBeDefined()
      expect(typeof formatIcons[key], `formatIcons['${key}'] must be a function`).toBe('function')
    }
  })
})

describe('Icon props', () => {
  it('accepts size prop and sets width/height', () => {
    const { container } = render(<IconDashboard size={32} />)
    const svg = container.querySelector('svg')!
    expect(svg.getAttribute('width')).toBe('32')
    expect(svg.getAttribute('height')).toBe('32')
  })

  it('defaults to size 20', () => {
    const { container } = render(<IconDashboard />)
    const svg = container.querySelector('svg')!
    expect(svg.getAttribute('width')).toBe('20')
    expect(svg.getAttribute('height')).toBe('20')
  })

  it('accepts className prop', () => {
    const { container } = render(<IconDashboard className="text-blue-500" />)
    const svg = container.querySelector('svg')!
    expect(svg.getAttribute('class')).toBe('text-blue-500')
  })
})
