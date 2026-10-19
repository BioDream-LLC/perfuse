import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { Provider } from 'react-redux'
import App from './App'
import { BrandingProvider } from './Branding'
import { ThemeProvider } from './Theme'
import { store } from './store'
import './index.css'
import { startLanguage } from './i18n/runtime'

const root = document.getElementById('root')
if (!root) throw new Error('no #root element')

// The language the person chose (or the browser's): its catalogue is loaded alongside the first screen, and text is
// translated as it is drawn.
void startLanguage()

createRoot(root).render(
  <StrictMode>
    <Provider store={store}>
      {/*
        Outside App so the sign-in page is branded too. Branding is the first thing a customer sees and
        an unbranded sign-in page gives the game away, so it cannot depend on being signed in.
      */}
      <BrandingProvider>
        <ThemeProvider>
          <App />
        </ThemeProvider>
      </BrandingProvider>
    </Provider>
  </StrictMode>,
)
