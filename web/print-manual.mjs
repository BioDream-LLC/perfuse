// print.mjs turns the generated HTML manual into a PDF.
//
// Why the browser prints it rather than a typesetter producing it:
//
// The PDF and the HTML are the same document. Any layout rule that keeps a configuration example from being split
// across a page break is written once, in the manual's own stylesheet, and applies to both. A separate typesetting
// path - asciidoctor-pdf, LaTeX - would mean two layouts to keep in agreement, and they would disagree quietly.
//
// It also needs nothing installed that this project does not already have. Playwright is here for the end-to-end tests
// and brings its own browser, so building the manual needs no Ruby, no gems and no TeX distribution.
//
// This file lives in web/ rather than beside the manual because Node resolves imports relative to the importing file,
// not the working directory. From docs/manual/ the playwright import fails; the dependency is in web/node_modules and
// the script has to sit where its dependency is.
// From @playwright/test rather than the bare playwright package: the test runner is what this project installs, and it
// re-exports the browser launchers. Importing 'playwright' fails because that package is not a dependency here.
import { chromium } from '@playwright/test'
import { pathToFileURL } from 'node:url'
import { resolve } from 'node:path'

const [, , inPath, outPath] = process.argv

if (!inPath || !outPath) {
  console.error('usage: node print.mjs <input.html> <output.pdf>')
  process.exit(1)
}

const browser = await chromium.launch()
const page = await browser.newPage()

// A file URL rather than a served page, so building the manual needs no server and works offline.
await page.goto(pathToFileURL(resolve(inPath)).href, { waitUntil: 'load' })

await page.pdf({
  path: resolve(outPath),
  format: 'A4',
  // printBackground so the code blocks, tables and notes keep the tint that distinguishes them. Without it every block
  // is white on white and the visual separation that makes a long reference readable is gone.
  printBackground: true,
  displayHeaderFooter: true,
  // Page numbers matter in a document this long: it is the only way a reader can be told where something is over the
  // phone. The chapter title goes in the header for the same reason.
  headerTemplate: `
    <div style="font: 8pt -apple-system, sans-serif; color: #6b7a8a; width: 100%;
                padding: 0 16mm; display: flex; justify-content: space-between;">
      <span>Perfuse Reference Manual</span>
      <span class="title"></span>
    </div>`,
  footerTemplate: `
    <div style="font: 8pt -apple-system, sans-serif; color: #6b7a8a; width: 100%;
                padding: 0 16mm; text-align: center;">
      <span class="pageNumber"></span> of <span class="totalPages"></span>
    </div>`,
  margin: { top: '20mm', bottom: '18mm', left: '16mm', right: '16mm' },
})

await browser.close()
console.log(`${outPath} written`)
